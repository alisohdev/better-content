package assets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaInspectorRejectsKnownMediaFamilyMismatchBeforeFFprobe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     []byte
		declared mediaType
	}{
		{name: "jpeg declared audio", data: []byte("\xff\xd8\xff"), declared: mediaAudio},
		{name: "png declared audio", data: []byte("\x89PNG\r\n\x1a\n"), declared: mediaAudio},
		{name: "webp declared audio", data: []byte("RIFF\x00\x00\x00\x00WEBP"), declared: mediaAudio},
		{name: "m4a declared video", data: []byte("xxxxftypM4A "), declared: mediaVideo},
		{name: "wav declared video", data: []byte("RIFF\x00\x00\x00\x00WAVE"), declared: mediaVideo},
		{name: "mp3 id3 declared video", data: []byte("ID3\x04\x00\x00"), declared: mediaVideo},
		{name: "mp3 frame declared video", data: []byte{0xff, 0xfb, 0x90, 0x64}, declared: mediaVideo},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "fixture.bin")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			inspector := newMediaInspector("must-not-run", "8.1.2", func(context.Context, string, []string, time.Duration, int) (ffprobeResult, error) {
				t.Fatal("ffprobe ran after the file signature already disproved the declared media type")
				return ffprobeResult{}, nil
			})
			_, err := inspector.inspect(context.Background(), path, test.declared, "fixture.bin", int64(len(test.data)))
			var failure *mediaInspectionError
			if !errors.As(err, &failure) || failure.failureCode != "MEDIA_TYPE_MISMATCH" {
				t.Fatalf("inspection error = %v, want MEDIA_TYPE_MISMATCH", err)
			}
		})
	}
}
