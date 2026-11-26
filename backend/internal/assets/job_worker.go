package assets

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path"
	"time"

	"bettercontent/backend/internal/workspace"
)

const (
	assetJobPollInterval      = time.Second
	assetJobHeartbeatInterval = 15 * time.Second
	assetJobDatabaseTimeout   = 5 * time.Second
	assetJobAttemptTimeout    = 10 * time.Minute
	assetWorkerMinTempBytes   = 1 << 30
)

type WorkerConfig struct {
	FFprobePath            string
	FFprobeExpectedVersion string
	TempDir                string
}

type assetWorker struct {
	*Service
	inspector         workerMediaInspector
	remote            workerRemoteAcquirer
	tempDir           string
	heartbeatInterval time.Duration
}

// RunAssetWorker starts the single-attempt-at-a-time supervisor. This entry
// point may be enabled only in the environment assigned to the Go worker.
func (s *Service) RunAssetWorker(ctx context.Context, cfg WorkerConfig) error {
	if cfg.TempDir == "" {
		cfg.TempDir = os.TempDir()
	}
	w := assetWorker{
		Service:           s,
		inspector:         newMediaInspector(cfg.FFprobePath, cfg.FFprobeExpectedVersion, nil),
		remote:            newControlledRemoteAcquirer(nil, nil, defaultRemoteAcquisitionLimits),
		tempDir:           cfg.TempDir,
		heartbeatInterval: assetJobHeartbeatInterval,
	}
	startup, cancel := context.WithTimeout(ctx, 45*time.Second)
	err := w.preflight(startup)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if w.logger != nil {
		w.logger.InfoContext(ctx, "assets.worker_ready")
	}
	w.run(ctx)
	return nil
}

func (w *assetWorker) preflight(ctx context.Context) error {
	if err := w.inspector.verifyRuntime(ctx); err != nil {
		return errors.New("configured ffprobe is unavailable or has the wrong version")
	}
	if err := verifyAssetWorkerTemp(w.tempDir, assetWorkerMinTempBytes); err != nil {
		return err
	}
	if err := w.database.PingContext(ctx); err != nil {
		return errors.New("worker PostgreSQL is unavailable")
	}
	// Require the reviewed schema before claiming anything.
	rows, err := w.database.QueryContext(ctx, "SELECT id, lease_owner, lease_expires_at FROM asset_jobs LIMIT 0")
	if err != nil {
		return errors.New("worker PostgreSQL job schema is unavailable")
	}
	rows.Close()
	if err := w.reapScratch(ctx, time.Now()); err != nil {
		return errors.New("could not clean Asset worker scratch directory")
	}
	if _, err := w.storage.listManagedObjects(ctx, permanentNamespace, "", 1); err != nil {
		return errors.New("private managed storage is unavailable")
	}
	return nil
}

func (w *assetWorker) run(ctx context.Context) {
	nextScratchSweep := time.Now().Add(workerScratchMaxAge)
	for ctx.Err() == nil {
		if time.Now().After(nextScratchSweep) {
			sweepCtx, cancel := context.WithTimeout(ctx, assetJobDatabaseTimeout)
			err := w.reapScratch(sweepCtx, time.Now())
			cancel()
			if err != nil && ctx.Err() == nil && w.logger != nil {
				w.logger.WarnContext(ctx, "assets.temp_cleanup_deferred", "errorCategory", processingUnavailable)
			}
			nextScratchSweep = time.Now().Add(workerScratchMaxAge)
		}
		claimCtx, cancel := context.WithTimeout(ctx, assetJobDatabaseTimeout)
		job, err := claimAssetJob(claimCtx, w.database)
		cancel()
		if err != nil && ctx.Err() == nil && w.logger != nil {
			w.logger.WarnContext(ctx, "assets.job_claim_failed", "errorCategory", processingUnavailable)
		}
		if job != nil {
			// A signal stops new claims. A committed attempt keeps its deadline and
			// renewals during drain, including when shutdown races the claim commit.
			w.runAttempt(context.WithoutCancel(ctx), job)
			continue
		}
		timer := time.NewTimer(assetJobPollInterval)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (w *assetWorker) runAttempt(parent context.Context, job *assetJob) {
	if job.exhausted {
		w.logJobResult(parent, *job, "assets.job_exhausted", processingUnavailable)
		w.cleanupJobStaging(parent, *job)
		return
	}
	ctx, cancel := context.WithTimeout(parent, assetJobAttemptTimeout)
	defer cancel()
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	lease := *job
	go func() {
		heartbeatDone <- w.maintainLease(heartbeatCtx, lease, cancel)
	}()

	mutation, err := w.handleJob(ctx, job)
	stopHeartbeat()
	heartbeatErr := <-heartbeatDone
	if heartbeatErr != nil || errors.Is(err, errAssetJobLeaseLost) {
		w.logJobResult(parent, *job, "assets.job_lease_lost", processingUnavailable)
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	var failure *assetJobFailure
	if err != nil {
		failure = normalizeAssetJobFailure(err)
		mutation = nil
	}
	finalizeCtx, cancelFinalize := context.WithTimeout(parent, assetJobDatabaseTimeout)
	defer cancelFinalize()
	if err := finalizeAssetJob(finalizeCtx, w.database, job, failure, mutation); err != nil {
		event := "assets.job_finalize_failed"
		if errors.Is(err, errAssetJobLeaseLost) {
			event = "assets.job_lease_lost"
		}
		w.logJobResult(parent, *job, event, processingUnavailable)
		return
	}
	switch {
	case failure == nil:
		w.logJobResult(parent, *job, "assets.job_completed", "")
	case failure.retryable && job.attempts < job.maxAttempts:
		w.logJobResult(parent, *job, "assets.job_retry_scheduled", failure.code)
	default:
		w.logJobResult(parent, *job, "assets.job_failed", failure.code)
	}
	w.cleanupJobStaging(parent, *job)
}

func (w *assetWorker) maintainLease(ctx context.Context, job assetJob, cancelAttempt context.CancelFunc) error {
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, assetJobDatabaseTimeout)
			err := withAssetJobLease(renewCtx, w.database, job, nil)
			cancel()
			if err != nil && ctx.Err() == nil {
				cancelAttempt()
				return err
			}
		}
	}
}

func (w *assetWorker) handleJob(ctx context.Context, job *assetJob) (assetJobMutation, error) {
	if !validJobAssetID(job.assetID) {
		return nil, &assetJobFailure{code: "INVALID_MEDIA"}
	}
	switch job.kind {
	case "PROCESS_UPLOAD", "INGEST_EXTERNAL_URL":
		scratch, err := makeWorkerAttemptScratch(w.tempDir, workerScratchFolder, job.owner)
		if err != nil {
			return nil, err
		}
		defer w.removeAttemptScratch(ctx, *job, scratch)
		attempt := *w
		attempt.tempDir = scratch
		if storage, ok := w.storage.(*filesystemStorage); ok {
			// Hard-link publication needs temp files on the storage volume.
			publication, err := makeWorkerAttemptScratch(storage.root.Name(), workerPublicationScratchFolder, job.owner)
			if err != nil {
				return nil, err
			}
			defer w.removeAttemptScratch(ctx, *job, publication)
			attemptStorage := *storage
			attemptStorage.publicationTempDir = path.Join(workerPublicationScratchFolder, workerScratchPrefix+job.owner)
			attemptService := *w.Service
			attemptService.storage = &attemptStorage
			attempt.Service = &attemptService
		}
		return attempt.processMedia(ctx, job)
	case "DELETE_ASSET":
		return w.prepareDeletionCleanup(ctx, job.assetID)
	case "EXPIRE_UPLOAD":
		return nil, w.expireUpload(ctx, *job)
	case "RECONCILE_STORAGE":
		return nil, w.reconcileStorage(ctx)
	default:
		return nil, &assetJobFailure{code: processingUnavailable}
	}
}

func (w *assetWorker) expireUpload(ctx context.Context, job assetJob) error {
	var workspaceID string
	err := w.database.QueryRowContext(ctx, "SELECT workspace_id FROM assets WHERE id=$1", job.assetID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	workspaceKey, err := workspace.ParseID(workspaceID)
	if err != nil {
		return err
	}
	var staging sql.NullString
	err = withAssetJobLease(ctx, w.database, job, func(ctx context.Context, tx *sql.Tx) error {
		if err := lockWorkspace(ctx, tx, workspaceKey); err != nil {
			return err
		}
		asset, err := findUploadForUpdate(ctx, tx, workspaceID, job.assetID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || asset.sourceType != "UPLOAD" {
			return err
		}
		if asset.status == "FAILED" {
			return tx.QueryRowContext(ctx, "SELECT CASE WHEN failure_code='UPLOAD_EXPIRED' THEN staging_key END FROM assets WHERE id=$1", job.assetID).Scan(&staging)
		}
		if asset.status != "PENDING" {
			return nil
		}
		finalized, err := hasDurableAssetJob(ctx, tx, "PROCESS_UPLOAD", job.assetID)
		if err != nil || finalized {
			return err
		}
		err = tx.QueryRowContext(ctx, `
   UPDATE assets SET status='FAILED',failure_code='UPLOAD_EXPIRED',updated_at=clock_timestamp()
   WHERE id=$1 AND created_at <= clock_timestamp()-$2*interval '1 second'
   RETURNING staging_key
  `, job.assetID, int64(uploadSessionLifetime/time.Second)).Scan(&staging)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil || !staging.Valid {
		return err
	}
	key, err := parseStagingKey(staging.String)
	if err != nil {
		return err
	}
	return deleteMissingStorageObject(func() error { return w.storage.deleteStagingObject(ctx, key) })
}

func (w *assetWorker) reconcileStorage(ctx context.Context) error {
	if err := w.reapScratch(ctx, time.Now()); err != nil {
		return err
	}
	// Use bounded pages and the same overall attempt deadline as media jobs.
	const pageSize = 100
	for _, namespace := range []storageNamespace{stagingNamespace, permanentNamespace} {
		for cursor := ""; ; {
			result, err := w.ReconcileManagedObjectPage(ctx, namespace, cursor, pageSize, time.Time{})
			if err != nil {
				return err
			}
			if result.NextCursor == "" {
				break
			}
			if result.NextCursor == cursor {
				return ErrStorageUnavailable
			}
			cursor = result.NextCursor
		}
	}
	for after := ""; ; {
		page, err := w.ReconcileReadyObjectIntegrity(ctx, after, pageSize)
		if err != nil || page.NextAfterID == "" {
			return err
		}
		after = page.NextAfterID
	}
}

func (w *assetWorker) cleanupJobStaging(parent context.Context, job assetJob) {
	if !job.cleanupStaging.Valid {
		return
	}
	key, err := parseStagingKey(job.cleanupStaging.String)
	if err != nil {
		w.logJobResult(parent, job, "assets.staging_cleanup_deferred", processingUnavailable)
		return
	}
	w.removeJobStaging(parent, job, key)
}

func (w *assetWorker) removeJobStaging(parent context.Context, job assetJob, key stagingKey) {
	ctx, cancel := context.WithTimeout(parent, assetJobDatabaseTimeout)
	defer cancel()
	if err := deleteMissingStorageObject(func() error { return w.storage.deleteStagingObject(ctx, key) }); err != nil {
		w.logJobResult(parent, job, "assets.staging_cleanup_deferred", processingUnavailable)
	}
}

func (w *assetWorker) logJobResult(ctx context.Context, job assetJob, event, category string) {
	if w.logger == nil {
		return
	}
	level := slog.LevelInfo
	if category != "" {
		level = slog.LevelWarn
	}
	w.logger.Log(ctx, level, event, "module", "assets", "jobId", job.id, "jobType", job.kind, "attempt", job.attempts, "errorCategory", category)
}
