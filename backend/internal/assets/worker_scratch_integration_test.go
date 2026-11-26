package assets

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkerScratchReapsOnlyOldAttemptDirectories(t *testing.T) {
	db := openWorkerTestDatabase(t)
	base := t.TempDir()
	old, err := makeWorkerAttemptScratch(base, workerScratchFolder, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	active, err := makeWorkerAttemptScratch(base, workerScratchFolder, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	jobID := addWorkerJob(t, db, "RECONCILE_STORAGE", uuid.NewString(), 5)
	job := claimWorkerFixture(t, db, jobID)
	oldButLeased, err := makeWorkerAttemptScratch(base, workerScratchFolder, job.owner)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(filepath.Dir(old), "unrelated")
	if err := os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{old, active, oldButLeased, unrelated} {
		if err := os.WriteFile(filepath.Join(dir, "partial-media"), []byte("temporary"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, dir := range []string{old, oldButLeased, unrelated} {
		if err := os.Chtimes(dir, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := reapWorkerScratch(context.Background(), db, base, workerScratchFolder, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old scratch remains: %v", err)
	}
	for _, dir := range []string{active, oldButLeased, unrelated} {
		if _, err := os.Stat(filepath.Join(dir, "partial-media")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerRemovesPartialAttemptFilesAfterCancellation(t *testing.T) {
	db := openWorkerTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, db)
	id, _ := seedWorkerAsset(t, db, userID, workspaceID, "EXTERNAL_URL", "PENDING", 0)
	job := claimWorkerFixture(t, db, addWorkerJob(t, db, "INGEST_EXTERNAL_URL", id, 5))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := assetWorker{
		Service: newService(db, newWorkerTestStorage(t), nil, nil),
		tempDir: t.TempDir(),
		remote: workerTestAcquirer(func(_ context.Context, _, _ string) (io.ReadCloser, error) {
			return io.NopCloser(&cancelDuringRead{cancel: cancel}), nil
		}),
	}
	if _, err := w.handleJob(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted download returned %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(w.tempDir, workerScratchFolder))
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled job left temporary directories: %v", err)
	}
}

type cancelDuringRead struct{ cancel context.CancelFunc }

func (r *cancelDuringRead) Read(buffer []byte) (int, error) {
	r.cancel()
	return copy(buffer, "partial download"), nil
}

func TestWorkerReapsFilesystemPublicationScratch(t *testing.T) {
	db := openWorkerTestDatabase(t)
	storage := newWorkerTestStorage(t).(*filesystemStorage)
	w := assetWorker{Service: newService(db, storage, nil, nil), tempDir: t.TempDir()}
	now := time.Now()
	old := filepath.Join(storage.root.Name(), workerPublicationScratchFolder, "attempt-"+uuid.NewString())
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "partial-object"), []byte("temporary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := w.reapScratch(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned publication scratch remains: %v", err)
	}
}
