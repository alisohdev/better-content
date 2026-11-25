package assets

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseMediaTypeUsesApprovedValues(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]mediaType{
		"IMAGE": mediaImage,
		"VIDEO": mediaVideo,
		"AUDIO": mediaAudio,
	} {
		got, err := parseMediaType(value)
		if err != nil || got != want || got.String() != value {
			t.Errorf("parseMediaType(%q) = (%v, %v), want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"", "image", "DOCUMENT"} {
		if _, err := parseMediaType(value); !errors.Is(err, errInvalidUploadInput) {
			t.Errorf("parseMediaType(%q) error = %v, want invalid input", value, err)
		}
	}
}

func TestNormalizeSafeNameTrimsWhitespaceAndPreservesCreatorUnicode(t *testing.T) {
	t.Parallel()

	got, err := normalizeSafeName("\uFEFF  فارسی creator  \uFEFF", 200)
	if err != nil {
		t.Fatal(err)
	}
	if want := safeName("فارسی creator"); got != want {
		t.Fatalf("normalizeSafeName() = %q, want %q", got, want)
	}
	if _, err := normalizeSafeName(strings.Repeat("ف", 200), 200); err != nil {
		t.Fatalf("200 Unicode code points should be accepted: %v", err)
	}
	if _, err := normalizeSafeName(strings.Repeat("ف", 201), 200); !errors.Is(err, errInvalidUploadInput) {
		t.Fatalf("201 Unicode code points error = %v, want invalid input", err)
	}
}

func TestSafeCreatorTextRejectsControlsBidiAndInvalidUTF8(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"line\nbreak",
		"creator\u0085name",
		"name\u202Etxt",
		"name\u2066txt\u2069",
		string([]byte{0xff}),
	} {
		if isSafeCreatorText(value) {
			t.Errorf("isSafeCreatorText(%q) = true, want false", value)
		}
	}
	if !isSafeCreatorText("English متن فارسی") {
		t.Fatal("ordinary mixed English/Persian text should be accepted")
	}
}

func TestNormalizeLeafFilenameRequiresSafeLeafAndBoundedCodePoints(t *testing.T) {
	t.Parallel()

	valid := "ف" + strings.Repeat("a", 250) + ".jpg"
	if utf8.RuneCountInString(valid) != 255 {
		t.Fatalf("fixture has %d code points, want 255", utf8.RuneCountInString(valid))
	}
	if _, err := normalizeLeafFilename(valid); err != nil {
		t.Fatalf("255-code-point leaf filename should be accepted: %v", err)
	}
	for _, value := range []string{
		"",
		".",
		"..",
		"folder/photo.jpg",
		`folder\photo.jpg`,
		strings.Repeat("a", 252) + ".jpg",
		"bad\u202Ename.jpg",
	} {
		if _, err := normalizeLeafFilename(value); !errors.Is(err, errInvalidUploadInput) {
			t.Errorf("normalizeLeafFilename(%q) error = %v, want invalid input", value, err)
		}
	}
}

func TestHasCompatibleUploadExtensionMatchesDeclaredMedia(t *testing.T) {
	t.Parallel()

	cases := []struct {
		filename string
		media    mediaType
		want     bool
	}{
		{"photo.jpg", mediaImage, true},
		{"photo.JPEG", mediaImage, true},
		{"photo.webp", mediaImage, true},
		{"photo.mp4", mediaVideo, true},
		{"sound.m4a", mediaAudio, true},
		{"sound.wav", mediaAudio, true},
		{"photo.mp4", mediaImage, false},
		{"sound.mp4", mediaAudio, true},
		{".jpg", mediaImage, false},
	}
	for _, test := range cases {
		filename, err := normalizeLeafFilename(test.filename)
		if err != nil {
			if test.want {
				t.Errorf("normalizeLeafFilename(%q): %v", test.filename, err)
			}
			continue
		}
		if got := hasCompatibleUploadExtension(filename, test.media); got != test.want {
			t.Errorf("hasCompatibleUploadExtension(%q, %s) = %v, want %v", filename, test.media, got, test.want)
		}
	}
}

func TestNormalizeUploadInputEnforcesMediaSpecificByteLimits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		media    mediaType
		filename string
		limit    int64
	}{
		{mediaImage, "photo.jpg", maxImageBytes},
		{mediaVideo, "clip.mp4", maxVideoBytes},
		{mediaAudio, "audio.mp3", maxAudioBytes},
	}
	for _, test := range cases {
		input := uploadInput{mediaType: test.media, displayName: "  valid name  ", originalFilename: test.filename, expectedSizeBytes: test.limit}
		got, err := normalizeUploadInput(input)
		if err != nil {
			t.Errorf("normalizeUploadInput at limit %d: %v", test.limit, err)
			continue
		}
		if got.expectedSize != test.limit || got.displayName != "valid name" {
			t.Errorf("normalized input = %#v, unexpected values", got)
		}
		input.expectedSizeBytes++
		if _, err := normalizeUploadInput(input); !errors.Is(err, errInvalidUploadInput) {
			t.Errorf("normalizeUploadInput above limit error = %v, want invalid input", err)
		}
	}

	emptyMIME := ""
	if _, err := normalizeUploadInput(uploadInput{mediaType: mediaImage, displayName: "name", originalFilename: "image.png", expectedSizeBytes: 1, browserMIMEType: &emptyMIME}); !errors.Is(err, errInvalidUploadInput) {
		t.Fatalf("empty browser MIME error = %v, want invalid input", err)
	}
	unknownMIME := "application/x-unknown"
	if _, err := normalizeUploadInput(uploadInput{mediaType: mediaImage, displayName: "name", originalFilename: "image.png", expectedSizeBytes: 1, browserMIMEType: &unknownMIME}); err != nil {
		t.Fatalf("non-empty browser MIME is a hint, not an eligibility check: %v", err)
	}
}

func TestMediaFormatMimeTypesAreNormalized(t *testing.T) {
	t.Parallel()

	cases := map[mediaFormat]string{
		mediaJPEG: "image/jpeg",
		mediaPNG:  "image/png",
		mediaWebP: "image/webp",
		mediaMP4:  "video/mp4",
		mediaMP3:  "audio/mpeg",
		mediaM4A:  "audio/mp4",
		mediaWAV:  "audio/wav",
	}
	for format, want := range cases {
		if got := format.mimeType(); got != want {
			t.Errorf("format %d MIME = %q, want %q", format, got, want)
		}
	}
	if mediaFormatInvalid.mimeType() != "" {
		t.Fatal("invalid media format must not have a MIME type")
	}
}
