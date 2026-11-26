package assets

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func withInspectionTempFile(ctx context.Context, directory string, source io.Reader, byteLimit, expectedSize int64, inspect func(string) (mediaInspection, error)) (mediaInspection, error) {
	if inspect == nil || expectedSize <= 0 {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	return withMediaTempFile(ctx, directory, source, byteLimit, expectedSize, func(path string, _ int64) (mediaInspection, error) {
		return inspect(path)
	})
}

// A zero expected size is used only to spool an acquisition of unknown length.
func withMediaTempFile(ctx context.Context, directory string, source io.Reader, byteLimit, expectedSize int64, inspect func(string, int64) (mediaInspection, error)) (mediaInspection, error) {
	if source == nil || byteLimit <= 0 {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	if expectedSize > byteLimit {
		return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
	}
	if err := ctx.Err(); err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if directory == "" {
		directory = os.TempDir()
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	file, err := os.CreateTemp(directory, "better-content-asset-*.tmp")
	if err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	written, err := copyWithContext(ctx, file, &io.LimitedReader{R: source, N: byteLimit + 1})
	if err != nil {
		_ = file.Close()
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if written > byteLimit {
		_ = file.Close()
		return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
	}
	if written == 0 || expectedSize > 0 && written != expectedSize {
		_ = file.Close()
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, errors.New("managed object size changed"))
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != written {
		_ = file.Close()
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if err := file.Close(); err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if err := ctx.Err(); err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	return inspect(absolutePath, written)
}
