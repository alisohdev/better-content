package assets

import (
	"errors"
	"os"
)

func verifyAssetWorkerTemp(directory string, minimumBytes uint64) error {
	file, err := os.CreateTemp(directory, "better-content-worker-preflight-*.tmp")
	if err != nil {
		return errors.New("worker temporary directory is unavailable or not writable")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write([]byte("ready"))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("worker temporary directory is unavailable or not writable")
	}
	free, err := workerTempFreeBytes(directory)
	if err != nil || free < minimumBytes {
		return errors.New("worker temporary disk needs at least 1 GiB available")
	}
	return nil
}
