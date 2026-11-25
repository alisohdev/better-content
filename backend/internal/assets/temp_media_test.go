package assets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if inspectCalls != 0 {
		t.Fatalf("inspector called %d times for rejected temp objects", inspectCalls)
	}
}
