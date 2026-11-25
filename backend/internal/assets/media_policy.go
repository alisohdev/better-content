package assets

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxImageBytes      int64 = 10 * 1024 * 1024
	maxVideoBytes      int64 = 500 * 1024 * 1024
	maxAudioBytes      int64 = 100 * 1024 * 1024
	maxImageSide             = 12_000
	maxImagePixels           = 60_000_000
	maxVideoSide             = 3_840
	maxVideoPixels           = 8_294_400
	maxVideoDurationMS       = 30 * 60 * 1_000
	maxAudioDurationMS       = 60 * 60 * 1_000
)

var errInvalidUploadInput = errors.New("invalid Asset upload input")

type mediaType uint8

const (
	mediaTypeInvalid mediaType = iota
	mediaImage
	mediaVideo
	mediaAudio
)

func parseMediaType(value string) (mediaType, error) {
	switch value {
	case "IMAGE":
		return mediaImage, nil
	case "VIDEO":
		return mediaVideo, nil
	case "AUDIO":
		return mediaAudio, nil
	default:
		return mediaTypeInvalid, errInvalidUploadInput
	}
}

func (value mediaType) String() string {
	switch value {
	case mediaImage:
		return "IMAGE"
	case mediaVideo:
		return "VIDEO"
	case mediaAudio:
		return "AUDIO"
	default:
		return ""
	}
}

func (value mediaType) maxUploadBytes() int64 {
	switch value {
	case mediaImage:
		return maxImageBytes
	case mediaVideo:
		return maxVideoBytes
	case mediaAudio:
		return maxAudioBytes
	default:
		return 0
	}
}

type mediaFormat uint8

const (
	mediaFormatInvalid mediaFormat = iota
	mediaJPEG
	mediaPNG
	mediaWebP
	mediaMP4
	mediaMP3
	mediaM4A
	mediaWAV
)

func (value mediaFormat) mimeType() string {
	switch value {
	case mediaJPEG:
		return "image/jpeg"
	case mediaPNG:
		return "image/png"
	case mediaWebP:
		return "image/webp"
	case mediaMP4:
		return "video/mp4"
	case mediaMP3:
		return "audio/mpeg"
	case mediaM4A:
		return "audio/mp4"
	case mediaWAV:
		return "audio/wav"
	default:
		return ""
	}
}

func (value mediaFormat) String() string {
	switch value {
	case mediaJPEG:
		return "JPEG"
	case mediaPNG:
		return "PNG"
	case mediaWebP:
		return "WEBP"
	case mediaMP4:
		return "MP4"
	case mediaMP3:
		return "MP3"
	case mediaM4A:
		return "M4A"
	case mediaWAV:
		return "WAV"
	default:
		return ""
	}
}

type safeName string
type leafFilename string

type uploadInput struct {
	mediaType         mediaType
	displayName       string
	originalFilename  string
	expectedSizeBytes int64
	browserMIMEType   *string
}

type validatedUploadInput struct {
	mediaType        mediaType
	displayName      safeName
	originalFilename leafFilename
	expectedSize     int64
	browserMIMEType  *string
}

func normalizeUploadInput(input uploadInput) (validatedUploadInput, error) {
	name, err := normalizeSafeName(input.displayName, 200)
	if err != nil || input.mediaType.maxUploadBytes() == 0 || input.expectedSizeBytes <= 0 ||
		input.expectedSizeBytes > input.mediaType.maxUploadBytes() {
		return validatedUploadInput{}, errInvalidUploadInput
	}

	filename, err := normalizeLeafFilename(input.originalFilename)
	if err != nil || !hasCompatibleUploadExtension(filename, input.mediaType) {
		return validatedUploadInput{}, errInvalidUploadInput
	}
	if input.browserMIMEType != nil && *input.browserMIMEType == "" {
		return validatedUploadInput{}, errInvalidUploadInput
	}

	var browserMIMEType *string
	if input.browserMIMEType != nil {
		value := *input.browserMIMEType
		browserMIMEType = &value
	}
	return validatedUploadInput{
		mediaType:        input.mediaType,
		displayName:      name,
		originalFilename: filename,
		expectedSize:     input.expectedSizeBytes,
		browserMIMEType:  browserMIMEType,
	}, nil
}

func normalizeSafeName(value string, maxCodePoints int) (safeName, error) {
	if !utf8.ValidString(value) {
		return "", errInvalidUploadInput
	}
	value = strings.TrimFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || character == '\uFEFF'
	})
	if !isSafeCreatorText(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > maxCodePoints {
		return "", errInvalidUploadInput
	}
	return safeName(value), nil
}

func normalizeLeafFilename(value string) (leafFilename, error) {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 || !isSafeCreatorText(value) ||
		strings.ContainsAny(value, `/\`) || value == "." || value == ".." {
		return "", errInvalidUploadInput
	}
	return leafFilename(value), nil
}

func isSafeCreatorText(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || isBidiControl(character) {
			return false
		}
	}
	return true
}

func hasCompatibleUploadExtension(filename leafFilename, media mediaType) bool {
	value := string(filename)
	dot := strings.LastIndex(value, ".")
	if dot <= 0 {
		return false
	}
	extension := strings.ToLower(value[dot:])
	switch media {
	case mediaImage:
		return extension == ".jpg" || extension == ".jpeg" || extension == ".png" || extension == ".webp"
	case mediaVideo:
		return extension == ".mp4"
	case mediaAudio:
		return extension == ".mp3" || extension == ".m4a" || extension == ".mp4" || extension == ".wav"
	default:
		return false
	}
}

func isBidiControl(character rune) bool {
	return character >= '\u202A' && character <= '\u202E' || character >= '\u2066' && character <= '\u2069'
}
