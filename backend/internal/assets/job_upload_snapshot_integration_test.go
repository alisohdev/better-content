package assets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadRetriesPublishTheSamePrivateSnapshot(t *testing.T) {
	db := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, db)
	ctx := context.Background()
	storage := newWorkerTestStorage(t)
	data, err := os.ReadFile("testdata/images/static-png.bin")
	if err != nil {
		t.Fatal(err)
	}
	id, browserStaging := seedWorkerAsset(t, db, userID, workspaceID, "UPLOAD", "PENDING", int64(len(data)))
	if err := storage.putStagingFromStream(ctx, browserStaging, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	jobID := addWorkerJob(t, db, "PROCESS_UPLOAD", id, 5)
	w := assetWorker{
		Service: newService(db, storage, nil, nil), inspector: newMediaInspector("", "", nil),
		tempDir: t.TempDir(), heartbeatInterval: assetJobHeartbeatInterval,
	}
	previous := claimWorkerFixture(t, db, jobID)
	result, err := w.handleJob(ctx, previous)
	if err != nil || result == nil {
		t.Fatalf("first attempt did not publish: %v", err)
	}

	// An S3 upload capability can still address the original staging key.
	if err := storage.deleteStagingObject(ctx, browserStaging); err != nil {
		t.Fatal(err)
	}
	if err := storage.putStagingFromStream(ctx, browserStaging, bytes.NewReader([]byte("different upload bytes"))); err != nil {
		t.Fatal(err)
	}
	expireWorkerJob(t, db, jobID)
	retry := claimWorkerFixture(t, db, jobID)
	if err := finalizeAssetJob(ctx, db, previous, nil, result); !errors.Is(err, errAssetJobLeaseLost) {
		t.Fatalf("stale commit returned %v", err)
	}
	w.runAttempt(ctx, retry)
	assertWorkerJobStatus(t, db, jobID, "COMPLETED", "")
	permanent, err := parsePermanentKey(assertWorkerReady(t, db, id, int64(len(data))))
	if err != nil {
		t.Fatal(err)
	}
	body, err := storage.openPermanentRead(ctx, permanent)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	published, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(published, data) {
		t.Fatal("publication did not preserve the original private snapshot")
	}
}

func TestWorkerRejectsPublicationConflictWithDifferentBytes(t *testing.T) {
	db := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, db)
	ctx := context.Background()
	const staged = "first snapshot"
	store := &conflictingPublication{assetStorage: newWorkerTestStorage(t), existing: "other snapshot"}
	id, staging := seedWorkerAsset(t, db, userID, workspaceID, "UPLOAD", "PENDING", int64(len(staged)))
	if err := store.putStagingFromStream(ctx, staging, bytes.NewBufferString(staged)); err != nil {
		t.Fatal(err)
	}
	jobID := addWorkerJob(t, db, "PROCESS_UPLOAD", id, 5)
	w := assetWorker{
		Service: newService(db, store, nil, nil), inspector: sameMetadataInspector{},
		tempDir: t.TempDir(), heartbeatInterval: assetJobHeartbeatInterval,
	}
	runWorkerFixture(t, &w, jobID)
	assertWorkerJobStatus(t, db, jobID, "FAILED", "INVALID_MEDIA")
	assertWorkerAssetFailure(t, db, id, "INVALID_MEDIA")
	scratch, err := os.ReadDir(filepath.Join(w.tempDir, workerScratchFolder))
	if err != nil || len(scratch) != 0 {
		t.Fatalf("failed job left temporary directories: %v", err)
	}
}

type conflictingPublication struct {
	assetStorage
	existing string
}

func (s *conflictingPublication) putPermanentFromStream(ctx context.Context, key permanentKey, _ io.Reader) error {
	if err := s.assetStorage.putPermanentFromStream(ctx, key, bytes.NewBufferString(s.existing)); err != nil {
		return err
	}
	return errStorageObjectExists
}

type sameMetadataInspector struct{ workerMediaInspector }

func (sameMetadataInspector) inspect(_ context.Context, _ string, declared mediaType, _ string, size int64) (mediaInspection, error) {
	return mediaInspection{mediaType: declared, mediaFormat: mediaPNG, sizeBytes: size, width: 2, height: 3}, nil
}
