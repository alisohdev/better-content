package assets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWithInspectionTempFileBoundsBytesAndCleansUp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var path string
	result, err := withInspectionTempFile(context.Background(), root, bytes.NewReader([]byte("safe")), 8, 4, func(filePath string) (mediaInspection, error) {
		path = filePath
		if !filepath.IsAbs(filePath) {
			t.Fatalf("inspection path is not absolute: %q", filePath)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(filePath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("inspection file does not have mode 0600: %v", err)
			}
		}
		contents, readErr := os.ReadFile(filePath)
		if readErr != nil || string(contents) != "safe" {
			t.Fatalf("temp contents=%q error=%v", contents, readErr)
		}
		return mediaInspection{mediaType: mediaImage, mediaFormat: mediaPNG, sizeBytes: 4, width: 1, height: 1}, nil
	})
	if err != nil || result.mediaFormat != mediaPNG {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection temp file remains after completion: stat err=%v", err)
	}
}

func TestWithInspectionTempFileRejectsOversizeAndSizeMismatch(t *testing.T) {
	t.Parallel()
	inspectCalls := 0
	inspect := func(string) (mediaInspection, error) {
		inspectCalls++
		return mediaInspection{}, nil
	}
	if _, err := withInspectionTempFile(context.Background(), t.TempDir(), bytes.NewReader([]byte("12345")), 4, 5, inspect); mustInspectionFailureCode(t, err) != "MEDIA_LIMIT_EXCEEDED" {
		t.Fatalf("oversize error = %v", err)
	}
	if _, err := withInspectionTempFile(context.Background(), t.TempDir(), bytes.NewReader([]byte("short")), 8, 4, inspect); mustInspectionFailureCode(t, err) != "INVALID_MEDIA" {
		t.Fatalf("size mismatch error = %v", err)
	}
	remote := bytes.NewReader([]byte("123456789"))
	if _, err := withMediaTempFile(context.Background(), t.TempDir(), remote, 4, 0, func(string, int64) (mediaInspection, error) {
		t.Error("oversize remote data reached inspection")
		return mediaInspection{}, nil
	}); mustInspectionFailureCode(t, err) != "MEDIA_LIMIT_EXCEEDED" || remote.Len() != 4 {
		t.Fatalf("expected limit plus one bytes consumed, remaining=%d, error=%v", remote.Len(), err)
	}
	if inspectCalls != 0 {
		t.Fatalf("inspector called %d times for rejected temp objects", inspectCalls)
	}
}
