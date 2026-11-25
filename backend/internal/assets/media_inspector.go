package assets

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type mediaInspector struct {
	ffprobe *ffprobeInspector
}

func newMediaInspector(ffprobePath, expectedVersion string, execute ffprobeExecutor) *mediaInspector {
	return &mediaInspector{ffprobe: newFFprobeInspector(ffprobePath, expectedVersion, execute)}
}

func (inspector *mediaInspector) verifyRuntime(ctx context.Context) error {
	return inspector.ffprobe.verifyRuntime(ctx)
}

func (inspector *mediaInspector) inspect(ctx context.Context, path string, declared mediaType, filename string, sizeBytes int64) (mediaInspection, error) {
	family, err := sniffMediaFamily(path)
	if err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if family != mediaTypeInvalid && family != declared {
		return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
	}
	if declared == mediaImage {
		file, err := os.Open(path)
		if err != nil {
			return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
		}
		defer file.Close()
		return inspectImage(ctx, io.Reader(file), declared, filename, sizeBytes)
	}
	return inspector.ffprobe.inspect(ctx, path, declared, filename, sizeBytes)
}

func sniffMediaFamily(path string) (mediaType, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return mediaTypeInvalid, errors.New("inspection file path is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return mediaTypeInvalid, err
	}
	defer file.Close()
	var header [16]byte
	read, err := file.Read(header[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return mediaTypeInvalid, err
	}
	value := header[:read]
	if len(value) >= 3 && string(value[:3]) == "\xff\xd8\xff" {
		return mediaImage, nil
	}
	if len(value) >= 8 && string(value[:8]) == "\x89PNG\r\n\x1a\n" {
		return mediaImage, nil
	}
	if len(value) >= 12 && string(value[:4]) == "RIFF" && string(value[8:12]) == "WEBP" {
		return mediaImage, nil
	}
	if len(value) >= 12 && string(value[4:8]) == "ftyp" {
		brand := strings.ToLower(strings.TrimSpace(string(value[8:12])))
		if strings.HasPrefix(brand, "m4a") {
			return mediaAudio, nil
		}
	}
	if len(value) >= 12 && string(value[:4]) == "RIFF" && string(value[8:12]) == "WAVE" {
		return mediaAudio, nil
	}
	if len(value) >= 3 && string(value[:3]) == "ID3" {
		return mediaAudio, nil
	}
	if len(value) >= 2 && value[0] == 0xff && value[1]&0xe0 == 0xe0 {
		return mediaAudio, nil
	}
	return mediaTypeInvalid, nil
}
