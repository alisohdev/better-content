package assets

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAssetWorkerPreflight(t *testing.T) {
	db := openWorkerTestDatabase(t)
	storage := newWorkerTestStorage(t)
	dir := t.TempDir()
	inspector := newMediaInspector("test-ffprobe", "7.1.5", func(_ context.Context, _ string, args []string, _ time.Duration, _ int) (ffprobeResult, error) {
		if len(args) != 1 || args[0] != "-version" {
			return ffprobeResult{}, errors.New("unexpected probe invocation")
		}
		return ffprobeResult{stdout: []byte("ffprobe version 7.1.5\n")}, nil
	})
	w := assetWorker{Service: newService(db, storage, time.Now, nil), inspector: inspector, tempDir: dir}
	ctx := context.Background()
	if err := w.preflight(ctx); err != nil {
		t.Fatal(err)
	}

	w.tempDir = filepath.Join(dir, "missing")
	if err := w.preflight(ctx); err == nil || !strings.Contains(err.Error(), "temporary directory") {
		t.Fatalf("missing temp directory returned %v", err)
	}
	w.tempDir = dir
	w.storage = &unavailableWorkerStorage{}
	if err := w.preflight(ctx); err == nil || !strings.Contains(err.Error(), "private managed storage") {
		t.Fatalf("unavailable storage returned %v", err)
	}
	inspector.ffprobe.expectedVersion = "7.1.4"
	if err := w.preflight(ctx); err == nil || !strings.Contains(err.Error(), "wrong version") {
		t.Fatalf("mismatched ffprobe returned %v", err)
	}
}

type unavailableWorkerStorage struct{ assetStorage }

func (*unavailableWorkerStorage) listManagedObjects(context.Context, storageNamespace, string, int) (managedStorageObjectPage, error) {
	return managedStorageObjectPage{}, errStorageUnavailable
}
