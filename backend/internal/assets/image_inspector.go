package assets

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"

	_ "golang.org/x/image/webp"
)

type mediaInspection struct {
	mediaType   mediaType
	mediaFormat mediaFormat
	sizeBytes   int64
	width       int
	height      int
	durationMS  int64
	audioCodec  string
	videoCodec  string
}

type mediaInspectionError struct {
	failureCode string
	retryable   bool
	err         error
}

func (e *mediaInspectionError) Error() string {
	if e.err == nil {
		return e.failureCode
	}
	return e.failureCode + ": " + e.err.Error()
}

func (e *mediaInspectionError) Unwrap() error { return e.err }

func inspectImage(ctx context.Context, source io.Reader, declared mediaType, filename string, sizeBytes int64) (mediaInspection, error) {
	if declared != mediaImage {
		return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
	}
	if sizeBytes > maxImageBytes {
		return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
	}
	if sizeBytes <= 0 || source == nil {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	data, err := io.ReadAll(io.LimitReader(source, maxImageBytes+1))
	if err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if int64(len(data)) > maxImageBytes {
		return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
	}
	if int64(len(data)) != sizeBytes {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	if err := ctx.Err(); err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if isAnimatedWebP(data) {
		return mediaInspection{}, imageInspectionFailure("UNSUPPORTED_MEDIA", false, nil)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if hasUnsupportedImageSignature(data) {
			return mediaInspection{}, imageInspectionFailure("UNSUPPORTED_MEDIA", false, err)
		}
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, err)
	}
	imageFormat, ok := normalizedImageFormat(format)
	if !ok {
		return mediaInspection{}, imageInspectionFailure("UNSUPPORTED_MEDIA", false, nil)
	}
	if !hasCompatibleUploadExtension(leafFilename(filename), mediaImage) || !imageExtensionMatches(filename, imageFormat) {
		return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	orientation := 1
	if imageFormat == mediaJPEG {
		orientation = jpegExifOrientation(data)
	}
	width, height := config.Width, config.Height
	if orientation >= 5 && orientation <= 8 {
		width, height = height, width
	}
	if width > maxImageSide || height > maxImageSide || int64(width)*int64(height) > maxImagePixels {
		return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
	}
	if _, decodedFormat, err := image.Decode(bytes.NewReader(data)); err != nil || decodedFormat != format {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, err)
	}
	return mediaInspection{mediaType: mediaImage, mediaFormat: imageFormat, sizeBytes: sizeBytes, width: width, height: height}, nil
}

func normalizedImageFormat(value string) (mediaFormat, bool) {
	switch value {
	case "jpeg":
		return mediaJPEG, true
	case "png":
		return mediaPNG, true
	case "webp":
		return mediaWebP, true
	default:
		return mediaFormatInvalid, false
	}
}

func imageExtensionMatches(filename string, format mediaFormat) bool {
	separator := strings.LastIndexByte(filename, '.')
	if separator < 0 {
		return false
	}
	extension := strings.ToLower(filename[separator:])
	switch format {
	case mediaJPEG:
		return extension == ".jpg" || extension == ".jpeg"
	case mediaPNG:
		return extension == ".png"
	case mediaWebP:
		return extension == ".webp"
	default:
		return false
	}
}

func imageInspectionFailure(code string, retryable bool, err error) error {
	return &mediaInspectionError{failureCode: code, retryable: retryable, err: err}
}

func hasUnsupportedImageSignature(data []byte) bool {
	return len(data) >= 4 && (bytes.Equal(data[:4], []byte{'I', 'I', 42, 0}) || bytes.Equal(data[:4], []byte{'M', 'M', 0, 42}))
}

func isAnimatedWebP(data []byte) bool {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	for offset := 12; offset+8 <= len(data); {
		tag := string(data[offset : offset+4])
		if tag == "ANIM" || tag == "ANMF" {
			return true
		}
		length := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		next := offset + 8 + length + (length & 1)
		if length < 0 || next < offset || next > len(data) {
			return false
		}
		offset = next
	}
	return false
}

func jpegExifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			return 1
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return 1
		}
		marker := data[offset]
		offset++
		if marker == 0xda || marker == 0xd9 {
			return 1
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if offset+2 > len(data) {
			return 1
		}
		segmentLength := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if segmentLength < 2 || offset+segmentLength > len(data) {
			return 1
		}
		segment := data[offset+2 : offset+segmentLength]
		if marker == 0xe1 && len(segment) >= 14 && bytes.Equal(segment[:6], []byte{'E', 'x', 'i', 'f', 0, 0}) {
			if value := readExifOrientation(segment[6:]); value >= 1 && value <= 8 {
				return value
			}
		}
		offset += segmentLength
	}
	return 1
}

func readExifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	offset := uint64(order.Uint32(tiff[4:8]))
	if offset+2 > uint64(len(tiff)) {
		return 1
	}
	count := uint64(order.Uint16(tiff[offset : offset+2]))
	for entry := uint64(0); entry < count; entry++ {
		start := offset + 2 + entry*12
		if start+12 > uint64(len(tiff)) {
			return 1
		}
		field := tiff[start : start+12]
		if order.Uint16(field[:2]) == 0x0112 && order.Uint16(field[2:4]) == 3 && order.Uint32(field[4:8]) == 1 {
			return int(order.Uint16(field[8:10]))
		}
	}
	return 1
}
