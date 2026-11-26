package assets

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"

	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAssetWorkerProcessesAllJobTypes(t *testing.T) {
	db := openWorkerTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, db)
	ctx := context.Background()
	storage := newWorkerTestStorage(t)
	data, err := os.ReadFile("testdata/images/static-png.bin")
	if err != nil {
		t.Fatal(err)
	}
	var acquires int
	w := assetWorker{
		Service:   newService(db, storage, time.Now, nil),
		inspector: newMediaInspector("", "", nil),
		remote: workerTestAcquirer(func(context.Context, string, string) (io.ReadCloser, error) {
			acquires++
			return io.NopCloser(bytes.NewReader(data)), nil
		}),
		tempDir:           t.TempDir(),
		heartbeatInterval: assetJobHeartbeatInterval,
	}

	uploadID, uploadKey := seedWorkerAsset(t, db, userID, workspaceID, "UPLOAD", "PENDING", int64(len(data)))
	if err := storage.putStagingFromStream(ctx, uploadKey, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	uploadJob := addWorkerJob(t, db, "PROCESS_UPLOAD", uploadID, 5)
	runWorkerFixture(t, &w, uploadJob)
	assertWorkerJobStatus(t, db, uploadJob, "COMPLETED", "")
	permanent := assertWorkerReady(t, db, uploadID, int64(len(data)))
	if _, exists, err := storage.getObjectMetadata(ctx, uploadKey.String()); err != nil || exists {
		t.Fatalf("upload staging remains: %v %v", exists, err)
	}

	ingestID, _ := seedWorkerAsset(t, db, userID, workspaceID, "EXTERNAL_URL", "PENDING", 0)
	ingestJob := addWorkerJob(t, db, "INGEST_EXTERNAL_URL", ingestID, 5)
	w.storage = &workerTestStorage{assetStorage: storage, failPublish: true}
	runWorkerFixture(t, &w, ingestJob)
	assertWorkerJobStatus(t, db, ingestJob, "PENDING", processingUnavailable)
	var snapshotKey string
	if err := db.QueryRow("SELECT staging_key FROM assets WHERE id=$1 AND declared_byte_size=$2", ingestID, len(data)).Scan(&snapshotKey); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := storage.getObjectMetadata(ctx, snapshotKey); err != nil || !exists {
		t.Fatalf("durable snapshot missing: %v %v", exists, err)
	}
	if _, err := db.Exec("UPDATE asset_jobs SET scheduled_at='-infinity' WHERE id=$1", ingestJob); err != nil {
		t.Fatal(err)
	}
	w.storage = storage
	runWorkerFixture(t, &w, ingestJob)
	assertWorkerJobStatus(t, db, ingestJob, "COMPLETED", "")
	assertWorkerReady(t, db, ingestID, int64(len(data)))
	if acquires != 1 {
		t.Fatalf("acquisition called %d times, want once", acquires)
	}

	expiredID, expiredKey := seedWorkerAsset(t, db, userID, workspaceID, "UPLOAD", "PENDING", int64(len(data)))
	if err := storage.putStagingFromStream(ctx, expiredKey, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	expiredJob := addWorkerJob(t, db, "EXPIRE_UPLOAD", expiredID, 5)
	runWorkerFixture(t, &w, expiredJob)
	assertWorkerJobStatus(t, db, expiredJob, "COMPLETED", "")
	assertWorkerAssetFailure(t, db, expiredID, "UPLOAD_EXPIRED")
	if _, exists, err := storage.getObjectMetadata(ctx, expiredKey.String()); err != nil || exists {
		t.Fatalf("expired staging remains: %v %v", exists, err)
	}

	if _, err := db.Exec("UPDATE assets SET status='DELETING',deleting_from_ready=1 WHERE id=$1", uploadID); err != nil {
		t.Fatal(err)
	}
	deleteJob := addWorkerJob(t, db, "DELETE_ASSET", uploadID, 5)
	runWorkerFixture(t, &w, deleteJob)
	assertWorkerJobStatus(t, db, deleteJob, "COMPLETED", "")
	var count int
	if err := db.QueryRow("SELECT count(*) FROM assets WHERE id=$1", uploadID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted asset remains: %d %v", count, err)
	}
	if _, exists, err := storage.getObjectMetadata(ctx, permanent); err != nil || exists {
		t.Fatalf("deleted permanent object remains: %v %v", exists, err)
	}

	orphan, err := newPermanentKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.putPermanentFromStream(ctx, orphan, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	w.clock = func() time.Time { return time.Now().Add(48 * time.Hour) }
	reconcileJob := addWorkerJob(t, db, "RECONCILE_STORAGE", uuid.NewString(), 5)
	runWorkerFixture(t, &w, reconcileJob)
	assertWorkerJobStatus(t, db, reconcileJob, "COMPLETED", "")
	if _, exists, err := storage.getObjectMetadata(ctx, orphan.String()); err != nil || exists {
		t.Fatalf("orphan remains: %v %v", exists, err)
	}
}

func TestAssetWorkerStaleMediaCannotCommitAndFinalCrashFailsAsset(t *testing.T) {
	db := openWorkerTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, db)
	ctx := context.Background()
	storage := newWorkerTestStorage(t)
	data, err := os.ReadFile("testdata/images/static-png.bin")
	if err != nil {
		t.Fatal(err)
	}
	assetID, key := seedWorkerAsset(t, db, userID, workspaceID, "UPLOAD", "PENDING", int64(len(data)))
	if err := storage.putStagingFromStream(ctx, key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	id := addWorkerJob(t, db, "PROCESS_UPLOAD", assetID, 2)
	w := assetWorker{Service: newService(db, storage, time.Now, nil), inspector: newMediaInspector("", "", nil), tempDir: t.TempDir(), heartbeatInterval: assetJobHeartbeatInterval}
	first := claimWorkerFixture(t, db, id)
	mutation, err := w.handleJob(ctx, first)
	if err != nil || mutation == nil {
		t.Fatalf("handle: %v", err)
	}
	expireWorkerJob(t, db, id)
	second := claimWorkerFixture(t, db, id)
	if err := finalizeAssetJob(ctx, db, first, nil, mutation); !errors.Is(err, errAssetJobLeaseLost) {
		t.Fatalf("stale media finalized: %v", err)
	}
	var status string
	if err := db.QueryRow("SELECT status FROM assets WHERE id=$1", assetID).Scan(&status); err != nil || status != "PROCESSING" {
		t.Fatalf("stale media changed asset: %s %v", status, err)
	}
	expireWorkerJob(t, db, id)
	recovered := claimWorkerFixture(t, db, id)
	if !recovered.exhausted || recovered.attempts != second.attempts {
		t.Fatalf("final crash was not recovered: %+v", recovered)
	}
	w.runAttempt(ctx, recovered)
	assertWorkerJobStatus(t, db, id, "FAILED", processingUnavailable)
	assertWorkerAssetFailure(t, db, assetID, processingUnavailable)
	if _, exists, err := storage.getObjectMetadata(ctx, key.String()); err != nil || exists {
		t.Fatalf("exhausted staging remains: %v %v", exists, err)
	}
}

func TestAssetWorkerDrainsActiveAttemptAndLeavesNextForRestart(t *testing.T) {
	db := openWorkerTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, db)
	storage := newWorkerTestStorage(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	assetID, _ := seedWorkerAsset(t, db, userID, workspaceID, "EXTERNAL_URL", "PENDING", 0)
	first := addWorkerJob(t, db, "INGEST_EXTERNAL_URL", assetID, 5)
	w := assetWorker{
		Service: newService(db, storage, time.Now, nil), tempDir: t.TempDir(), heartbeatInterval: 10 * time.Millisecond,
		remote: workerTestAcquirer(func(ctx context.Context, _ string, _ string) (io.ReadCloser, error) {
			close(started)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return nil, &RemoteAcquisitionError{FailureCode: "UNSAFE_MEDIA_URL"}
			}
		}),
	}
	go func() { defer close(done); w.run(ctx) }()
	t.Cleanup(func() { stop(); releaseOnce.Do(func() { close(release) }); <-done })
	waitWorkerChannel(t, started)
	next := addWorkerJob(t, db, "RECONCILE_STORAGE", uuid.NewString(), 5)
	stop()
	var initial, latest time.Time
	if err := db.QueryRow("SELECT lease_expires_at FROM asset_jobs WHERE id=$1", first).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !latest.After(initial); {
		if time.Now().After(deadline) {
			t.Fatal("heartbeat stopped during graceful drain")
		}
		if err := db.QueryRow("SELECT lease_expires_at FROM asset_jobs WHERE id=$1", first).Scan(&latest); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	releaseOnce.Do(func() { close(release) })
	waitWorkerChannel(t, done)
	assertWorkerJobStatus(t, db, first, "FAILED", "UNSAFE_MEDIA_URL")
	assertWorkerAssetFailure(t, db, assetID, "UNSAFE_MEDIA_URL")
	scratch, err := os.ReadDir(filepath.Join(w.tempDir, workerScratchFolder))
	if err != nil || len(scratch) != 0 {
		t.Fatalf("drained job left temporary directories: %v", err)
	}
	assertWorkerJobStatus(t, db, next, "PENDING", "")
	runWorkerFixture(t, &w, next)
	assertWorkerJobStatus(t, db, next, "COMPLETED", "")
}

func waitWorkerChannel(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach expected state")
	}
}

func newWorkerTestStorage(t *testing.T) assetStorage {
	t.Helper()
	storage, err := OpenDevelopmentFilesystemStorage(t.TempDir(), "http://localhost", bytes.Repeat([]byte("s"), 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	return storage.storage
}

type workerTestAcquirer func(context.Context, string, string) (io.ReadCloser, error)

func (f workerTestAcquirer) Acquire(ctx context.Context, url, kind string) (io.ReadCloser, error) {
	return f(ctx, url, kind)
}

type workerTestStorage struct {
	assetStorage
	failPublish bool
}

func (s *workerTestStorage) putPermanentFromStream(ctx context.Context, key permanentKey, reader io.Reader) error {
	if s.failPublish {
		return errStorageUnavailable
	}
	return s.assetStorage.putPermanentFromStream(ctx, key, reader)
}

func seedWorkerAsset(t *testing.T, db *sql.DB, userID, workspaceID, source, status string, size int64) (string, stagingKey) {
	t.Helper()
	id := uuid.NewString()
	key, err := newStagingKey()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
 INSERT INTO assets(id,workspace_id,created_by_user_id,media_type,source_type,status,
 display_name,original_filename,source_url,source_host,staging_key,declared_byte_size,created_at)
 VALUES ($1,$2,$3,'IMAGE',$4,$5,'worker fixture',CASE WHEN $4='UPLOAD' THEN 'fixture.png' END,
 CASE WHEN $4='EXTERNAL_URL' THEN 'https://media.example/fixture.png' END,CASE WHEN $4='EXTERNAL_URL' THEN 'media.example' END,
 $6,NULLIF($7,0),clock_timestamp()-interval '25 hours')
 `, id, workspaceID, userID, source, status, key.String(), size)
	if err != nil {
		t.Fatal(err)
	}
	return id, key
}

func claimWorkerFixture(t *testing.T, db *sql.DB, id string) *assetJob {
	t.Helper()
	job, err := claimAssetJob(context.Background(), db)
	if err != nil || job == nil || job.id != id {
		t.Fatalf("claim %s: %+v %v", id, job, err)
	}
	return job
}

func runWorkerFixture(t *testing.T, w *assetWorker, id string) {
	t.Helper()
	w.runAttempt(context.Background(), claimWorkerFixture(t, w.database, id))
}

func assertWorkerJobStatus(t *testing.T, db *sql.DB, id, status, code string) {
	t.Helper()
	var gotStatus, gotCode string
	err := db.QueryRow("SELECT status,COALESCE(failure_code,'') FROM asset_jobs WHERE id=$1", id).Scan(&gotStatus, &gotCode)
	if err != nil || gotStatus != status || gotCode != code {
		t.Fatalf("job status = %s %s (%v), want %s %s", gotStatus, gotCode, err, status, code)
	}
}

func assertWorkerReady(t *testing.T, db *sql.DB, id string, size int64) string {
	t.Helper()
	var permanent string
	err := db.QueryRow(`
 SELECT permanent_key FROM assets WHERE id=$1 AND status='READY' AND byte_size=$2
 AND media_format='PNG' AND detected_mime_type='image/png' AND width=2 AND height=3 AND staging_key IS NULL
 `, id, size).Scan(&permanent)
	if err != nil {
		t.Fatalf("asset not ready with expected metadata: %v", err)
	}
	return permanent
}

func assertWorkerAssetFailure(t *testing.T, db *sql.DB, id, code string) {
	t.Helper()
	var got string
	if err := db.QueryRow("SELECT failure_code FROM assets WHERE id=$1 AND status='FAILED'", id).Scan(&got); err != nil || got != code {
		t.Fatalf("asset failure %q (%v), want %q", got, err, code)
	}
}
