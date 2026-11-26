package assets

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	workerScratchFolder            = "better-content-asset-worker"
	workerPublicationScratchFolder = "better-content-asset-publication"
	workerScratchPrefix            = "attempt-"
	workerScratchMaxAge            = time.Hour
)

func openWorkerScratch(base, folder string) (*os.Root, error) {
	directory := filepath.Join(base, folder)
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("invalid Asset worker scratch directory")
	}
	return os.OpenRoot(directory)
}

func makeWorkerAttemptScratch(base, folder, owner string) (string, error) {
	if !validJobAssetID(owner) {
		return "", errAssetJobLeaseLost
	}
	root, err := openWorkerScratch(base, folder)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := workerScratchPrefix + owner
	if err := root.Mkdir(name, 0700); err != nil {
		return "", err
	}
	return filepath.Join(root.Name(), name), nil
}

// An attempt has a fresh directory and a ten-minute deadline. Only reap
// worker-owned directories older than an hour, including when different
// workers share the same host temp disk. Root-relative deletion never follows
// links outside the scratch directory.
func reapWorkerScratch(ctx context.Context, db *sql.DB, base, folder string, now time.Time) error {
	root, err := openWorkerScratch(base, folder)
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	cutoff := now.Add(-workerScratchMaxAge)
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			owner, ok := strings.CutPrefix(entry.Name(), workerScratchPrefix)
			if !ok || !validJobAssetID(owner) || !entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.ModTime().Before(cutoff) {
				continue
			}
			var owned bool
			if err := db.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM asset_jobs WHERE status='RUNNING' AND lease_owner=$1
					AND lease_expires_at > clock_timestamp()
				)
			`, owner).Scan(&owned); err != nil {
				return err
			}
			if !owned {
				if err := root.RemoveAll(entry.Name()); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (w *assetWorker) reapScratch(ctx context.Context, now time.Time) error {
	if err := reapWorkerScratch(ctx, w.database, w.tempDir, workerScratchFolder, now); err != nil {
		return err
	}
	if storage, ok := w.storage.(*filesystemStorage); ok {
		return reapWorkerScratch(ctx, w.database, storage.root.Name(), workerPublicationScratchFolder, now)
	}
	return nil
}

func (w *assetWorker) removeAttemptScratch(ctx context.Context, job assetJob, directory string) {
	if err := os.RemoveAll(directory); err != nil {
		w.logJobResult(ctx, job, "assets.temp_cleanup_deferred", processingUnavailable)
	}
}
