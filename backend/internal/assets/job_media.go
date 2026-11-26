package assets

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
)

type workerMediaInspector interface {
	verifyRuntime(context.Context) error
	inspect(context.Context, string, mediaType, string, int64) (mediaInspection, error)
}

type workerRemoteAcquirer interface {
	Acquire(context.Context, string, string) (io.ReadCloser, error)
}

type processingAsset struct {
	mediaType, sourceType, status           string
	sourceURL, filename, staging, permanent sql.NullString
	declared                                sql.NullInt64
}

func (w *assetWorker) prepareMedia(ctx context.Context, job assetJob) (processingAsset, error) {
	var asset processingAsset
	err := withAssetJobLease(ctx, w.database, job, func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
  SELECT media_type,source_type,status,source_url,original_filename,staging_key,permanent_key,declared_byte_size
  FROM assets WHERE id=$1 FOR UPDATE
  `, job.assetID).Scan(&asset.mediaType, &asset.sourceType, &asset.status, &asset.sourceURL, &asset.filename, &asset.staging, &asset.permanent, &asset.declared)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if asset.status != "PENDING" && asset.status != "PROCESSING" {
			return nil
		}
		if job.kind == "PROCESS_UPLOAD" && asset.sourceType != "UPLOAD" ||
			job.kind == "INGEST_EXTERNAL_URL" && asset.sourceType != "EXTERNAL_URL" {
			return &assetJobFailure{code: "INVALID_MEDIA"}
		}
		if asset.status == "PENDING" {
			_, err = tx.ExecContext(ctx, "UPDATE assets SET status='PROCESSING',updated_at=clock_timestamp() WHERE id=$1", job.assetID)
		}
		return err
	})
	return asset, err
}

func (w *assetWorker) processMedia(ctx context.Context, job *assetJob) (assetJobMutation, error) {
	asset, err := w.prepareMedia(ctx, *job)
	if err != nil || asset.status != "PENDING" && asset.status != "PROCESSING" {
		return nil, err
	}
	declared, err := parseMediaType(asset.mediaType)
	if err != nil {
		return nil, &assetJobFailure{code: "INVALID_MEDIA"}
	}
	if asset.sourceType == "EXTERNAL_URL" && !asset.declared.Valid {
		asset.staging, asset.declared, err = w.acquireSnapshot(ctx, *job, asset.sourceURL.String, declared)
		if err != nil {
			return nil, err
		}
	}
	staging, err := parseStagingKey(asset.staging.String)
	if err != nil || !asset.declared.Valid {
		return nil, &assetJobFailure{code: "INVALID_MEDIA"}
	}

	var permanent permanentKey
	var frozenStaging stagingKey
	var stagedHash [sha256.Size]byte
	staged, err := w.inspectManaged(ctx, staging.String(), declared, asset.filename.String, asset.declared.Int64,
		func() (io.ReadCloser, error) { return w.storage.openStagingRead(ctx, staging) },
		func(path string) error {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			stagedHash, err = hashManagedMedia(ctx, file)
			if err != nil {
				return err
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return err
			}
			permanent, frozenStaging, err = w.reservePermanent(ctx, *job, asset, staging, file)
			if err != nil {
				return err
			}
			err = w.storage.putPermanentFromStream(ctx, permanent, file)
			if errors.Is(err, errStorageObjectExists) {
				// A precondition failure means only that bytes already exist.
				// The read below proves whether they belong to this snapshot.
				return nil
			}
			return err
		})
	if err != nil {
		return nil, err
	}
	published, err := w.inspectManaged(ctx, permanent.String(), declared, asset.filename.String, 0,
		func() (io.ReadCloser, error) { return w.storage.openPermanentRead(ctx, permanent) },
		func(path string) error {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			digest, err := hashManagedMedia(ctx, file)
			if err != nil {
				return err
			}
			if digest != stagedHash {
				return &assetJobFailure{code: "INVALID_MEDIA"}
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	if staged != published {
		return nil, &assetJobFailure{code: "INVALID_MEDIA"}
	}

	codecs := strings.Trim(strings.Join([]string{published.videoCodec, published.audioCodec}, ","), ",")
	return func(ctx context.Context, tx *sql.Tx) error {
		outcome, err := tx.ExecContext(ctx, `
  UPDATE assets SET status='READY',staging_key=NULL,byte_size=$4,detected_mime_type=$5,media_format=$6,
    width=NULLIF($7,0),height=NULLIF($8,0),duration_ms=NULLIF($9,0),codecs=NULLIF($10,''),updated_at=clock_timestamp()
  WHERE id=$1 AND status='PROCESSING' AND staging_key=$2 AND permanent_key=$3
  `, job.assetID, frozenStaging.String(), permanent.String(), published.sizeBytes, published.mediaFormat.mimeType(),
			published.mediaFormat.String(), published.width, published.height, published.durationMS, codecs)
		if err := requireAssetJobUpdate(outcome, err); err != nil {
			return err
		}
		job.cleanupStaging = sql.NullString{String: frozenStaging.String(), Valid: true}
		return nil
	}, nil
}

func (w *assetWorker) acquireSnapshot(ctx context.Context, job assetJob, source string, declared mediaType) (sql.NullString, sql.NullInt64, error) {
	var keyValue sql.NullString
	var sizeValue sql.NullInt64
	key, err := newStagingKey()
	if err != nil {
		return keyValue, sizeValue, err
	}
	body, err := w.remote.Acquire(ctx, source, declared.String())
	if err != nil {
		return keyValue, sizeValue, err
	}
	defer body.Close()

	// Spool a bounded, seekable body for storage adapters. An attempt-private
	// key keeps uncommitted downloads separate from the next winning snapshot.
	_, err = withMediaTempFile(ctx, w.tempDir, body, declared.maxUploadBytes(), 0, func(path string, size int64) (mediaInspection, error) {
		file, err := os.Open(path)
		if err != nil {
			return mediaInspection{}, err
		}
		defer file.Close()
		if err := w.storage.putStagingFromStream(ctx, key, file); err != nil {
			return mediaInspection{}, err
		}
		err = withAssetJobLease(ctx, w.database, job, func(ctx context.Context, tx *sql.Tx) error {
			outcome, err := tx.ExecContext(ctx, `
    UPDATE assets SET staging_key=$2,declared_byte_size=$3,updated_at=clock_timestamp()
    WHERE id=$1 AND status='PROCESSING' AND source_type='EXTERNAL_URL' AND declared_byte_size IS NULL
   `, job.assetID, key.String(), size)
			return requireAssetJobUpdate(outcome, err)
		})
		if err == nil {
			keyValue = sql.NullString{String: key.String(), Valid: true}
			sizeValue = sql.NullInt64{Int64: size, Valid: true}
		}
		// If commit acknowledgement is lost, reconciliation will recheck ownership
		// before deleting the attempt object.
		return mediaInspection{}, err
	})
	return keyValue, sizeValue, err
}

func (w *assetWorker) reservePermanent(ctx context.Context, job assetJob, asset processingAsset, staging stagingKey, inspected *os.File) (permanentKey, stagingKey, error) {
	if asset.permanent.Valid {
		key, err := parsePermanentKey(asset.permanent.String)
		if err == nil {
			err = withAssetJobLease(ctx, w.database, job, nil)
		}
		return key, staging, err
	}
	key, err := newPermanentKey()
	if err != nil {
		return permanentKey{}, stagingKey{}, err
	}
	frozen := staging
	if asset.sourceType == "UPLOAD" {
		frozen, err = newStagingKey()
		if err != nil {
			return permanentKey{}, stagingKey{}, err
		}
		// A browser capability may still write its original staging key. Pin
		// the inspected bytes at a private key before reserving publication.
		if err := w.storage.putStagingFromStream(ctx, frozen, inspected); err != nil {
			return permanentKey{}, stagingKey{}, err
		}
		if _, err := inspected.Seek(0, io.SeekStart); err != nil {
			return permanentKey{}, stagingKey{}, err
		}
	}
	err = withAssetJobLease(ctx, w.database, job, func(ctx context.Context, tx *sql.Tx) error {
		outcome, err := tx.ExecContext(ctx, `
			UPDATE assets SET permanent_key=$2,staging_key=$3,updated_at=clock_timestamp()
			WHERE id=$1 AND status='PROCESSING' AND staging_key=$4 AND permanent_key IS NULL
		`, job.assetID, key.String(), frozen.String(), staging.String())
		return requireAssetJobUpdate(outcome, err)
	})
	if err != nil {
		// The reservation may have committed. Orphan reconciliation rechecks
		// ownership before removing any unclaimed snapshot.
		return permanentKey{}, stagingKey{}, err
	}
	if frozen != staging {
		w.removeJobStaging(ctx, job, staging)
	}
	return key, frozen, nil
}

func (w *assetWorker) inspectManaged(ctx context.Context, key string, declared mediaType, filename string, stagedSize int64,
	open func() (io.ReadCloser, error), afterInspect func(string) error) (mediaInspection, error) {
	metadata, found, err := w.storage.getObjectMetadata(ctx, key)
	if err != nil {
		return mediaInspection{}, err
	}
	if !found {
		if stagedSize > 0 {
			return mediaInspection{}, &assetJobFailure{code: "INVALID_MEDIA"}
		}
		return mediaInspection{}, errStorageUnavailable
	}
	if stagedSize > 0 && metadata.sizeBytes != stagedSize {
		return mediaInspection{}, &assetJobFailure{code: "INVALID_MEDIA"}
	}
	if metadata.sizeBytes <= 0 {
		return mediaInspection{}, &assetJobFailure{code: "INVALID_MEDIA"}
	}
	if metadata.sizeBytes > declared.maxUploadBytes() {
		return mediaInspection{}, &assetJobFailure{code: "MEDIA_TOO_LARGE"}
	}
	body, err := open()
	if err != nil {
		if stagedSize > 0 && errors.Is(err, errStorageNotFound) {
			return mediaInspection{}, &assetJobFailure{code: "INVALID_MEDIA"}
		}
		return mediaInspection{}, err
	}
	defer body.Close()
	return withInspectionTempFile(ctx, w.tempDir, body, declared.maxUploadBytes(), metadata.sizeBytes, func(path string) (mediaInspection, error) {
		result, err := w.inspector.inspect(ctx, path, declared, filename, metadata.sizeBytes)
		if err != nil {
			return mediaInspection{}, err
		}
		if result.mediaType != declared || result.mediaFormat.mimeType() == "" || result.sizeBytes != metadata.sizeBytes {
			return mediaInspection{}, &assetJobFailure{code: "INVALID_MEDIA"}
		}
		if err := afterInspect(path); err != nil {
			return mediaInspection{}, err
		}
		return result, nil
	})
}

func hashManagedMedia(ctx context.Context, file *os.File) ([sha256.Size]byte, error) {
	hash := sha256.New()
	_, err := copyWithContext(ctx, hash, file)
	var digest [sha256.Size]byte
	hash.Sum(digest[:0])
	return digest, err
}

func requireAssetJobUpdate(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("Asset lifecycle changed during job")
	}
	return nil
}

func normalizeAssetJobFailure(err error) *assetJobFailure {
	var failure *assetJobFailure
	if errors.As(err, &failure) {
		return failure
	}
	var remote *RemoteAcquisitionError
	if errors.As(err, &remote) {
		return &assetJobFailure{code: remote.FailureCode, retryable: remote.Retryable}
	}
	var inspection *mediaInspectionError
	if errors.As(err, &inspection) {
		return &assetJobFailure{code: inspection.failureCode, retryable: inspection.retryable}
	}
	return &assetJobFailure{code: processingUnavailable, retryable: true}
}

func validJobAssetID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && validUUID(id) && id.String() == value
}
