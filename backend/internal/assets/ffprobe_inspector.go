package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultFFprobeTimeout    = 30 * time.Second
	defaultFFprobeMaxOutput  = 256 * 1024
	maxJSONSafeInteger       = 9_007_199_254_740_991
	maxFFprobeDiagnosticSize = 256 * 1024
)

type ffprobeResult struct {
	exitCode int
	stdout   []byte
	stderr   []byte
}

type ffprobeExecutor func(context.Context, string, []string, time.Duration, int) (ffprobeResult, error)

type ffprobeInspector struct {
	executablePath  string
	expectedVersion string
	timeout         time.Duration
	maxOutputBytes  int
	execute         ffprobeExecutor
}

type ffprobeDocument struct {
	streams []map[string]any
	format  map[string]any
}

func newFFprobeInspector(executablePath, expectedVersion string, execute ffprobeExecutor) *ffprobeInspector {
	if execute == nil {
		execute = executeFFprobe
	}
	return &ffprobeInspector{executablePath: executablePath, expectedVersion: expectedVersion, timeout: defaultFFprobeTimeout, maxOutputBytes: defaultFFprobeMaxOutput, execute: execute}
}

func (inspector *ffprobeInspector) verifyRuntime(ctx context.Context) error {
	if strings.TrimSpace(inspector.executablePath) == "" || strings.TrimSpace(inspector.expectedVersion) == "" {
		return imageInspectionFailure("PROCESSING_UNAVAILABLE", true, errors.New("ffprobe path and expected version are required"))
	}
	result, err := inspector.execute(ctx, inspector.executablePath, []string{"-version"}, inspector.timeout, inspector.maxOutputBytes)
	if err != nil || result.exitCode != 0 {
		return imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	version := ffprobeVersion(result.stdout)
	if version == "" || version != inspector.expectedVersion {
		return imageInspectionFailure("PROCESSING_UNAVAILABLE", true, errors.New("ffprobe version does not match configured version"))
	}
	return nil
}

func (inspector *ffprobeInspector) inspect(ctx context.Context, filePath string, declared mediaType, filename string, sizeBytes int64) (mediaInspection, error) {
	if declared != mediaVideo && declared != mediaAudio {
		return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
	}
	if !filepath.IsAbs(filePath) || strings.ContainsRune(filePath, '\x00') {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, nil)
	}
	limit := maxAudioBytes
	if declared == mediaVideo {
		limit = maxVideoBytes
	}
	if sizeBytes > limit {
		return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
	}
	if sizeBytes <= 0 {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	file, err := os.Stat(filePath)
	if err != nil || !file.Mode().IsRegular() || file.Size() != sizeBytes {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if err := ctx.Err(); err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	args := []string{"-v", "error", "-hide_banner", "-print_format", "json", "-show_streams", "-show_format", "-protocol_whitelist", "file", filePath}
	result, err := inspector.execute(ctx, inspector.executablePath, args, inspector.timeout, inspector.maxOutputBytes)
	if err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	if result.exitCode != 0 {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, errors.New("ffprobe rejected media bytes"))
	}
	document, err := parseFFprobeDocument(result.stdout)
	if err != nil {
		return mediaInspection{}, imageInspectionFailure("PROCESSING_UNAVAILABLE", true, err)
	}
	return normalizeFFprobeDocument(document, declared, filename, sizeBytes)
}

func executeFFprobe(parent context.Context, executable string, args []string, timeout time.Duration, maxOutputBytes int) (ffprobeResult, error) {
	if timeout <= 0 {
		timeout = defaultFFprobeTimeout
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultFFprobeMaxOutput
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = ffprobeEnvironment(os.Environ())
	stdout := &limitedCommandOutput{limit: maxOutputBytes}
	stderr := &limitedCommandOutput{limit: maxFFprobeDiagnosticSize}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ffprobeResult{}, errors.New("ffprobe timed out")
	}
	if stdout.exceeded || stderr.exceeded {
		return ffprobeResult{}, errors.New("ffprobe output limit exceeded")
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return ffprobeResult{exitCode: exitError.ExitCode(), stdout: stdout.data.Bytes(), stderr: stderr.data.Bytes()}, nil
		}
		return ffprobeResult{}, err
	}
	return ffprobeResult{exitCode: 0, stdout: stdout.data.Bytes(), stderr: stderr.data.Bytes()}, nil
}

type limitedCommandOutput struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func (output *limitedCommandOutput) Write(value []byte) (int, error) {
	remaining := output.limit - output.data.Len()
	if remaining <= 0 {
		output.exceeded = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = output.data.Write(value[:remaining])
		output.exceeded = true
		return len(value), nil
	}
	return output.data.Write(value)
}

func ffprobeEnvironment(environment []string) []string {
	allowed := map[string]bool{"PATH": true, "SYSTEMROOT": true, "WINDIR": true, "TMP": true, "TEMP": true, "LANG": true, "LC_ALL": true}
	result := make([]string, 0, len(allowed))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		canonical := strings.ToUpper(key)
		if allowed[canonical] {
			result = append(result, entry)
		}
	}
	return result
}

func ffprobeVersion(output []byte) string {
	matched := regexp.MustCompile(`(?m)^ffprobe version ([^\s]+)`).FindSubmatch(output)
	if len(matched) != 2 {
		return ""
	}
	return string(matched[1])
}

func parseFFprobeDocument(raw []byte) (ffprobeDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var top map[string]json.RawMessage
	if err := decoder.Decode(&top); err != nil || top == nil || len(top) != 2 {
		return ffprobeDocument{}, errors.New("ffprobe JSON has invalid top-level shape")
	}
	streamsRaw, streamsFound := top["streams"]
	formatRaw, formatFound := top["format"]
	if !streamsFound || !formatFound {
		return ffprobeDocument{}, errors.New("ffprobe JSON is missing required fields")
	}
	var streams []map[string]any
	var format map[string]any
	if err := json.Unmarshal(streamsRaw, &streams); err != nil || streams == nil {
		return ffprobeDocument{}, errors.New("ffprobe streams have invalid shape")
	}
	if err := json.Unmarshal(formatRaw, &format); err != nil || format == nil {
		return ffprobeDocument{}, errors.New("ffprobe format has invalid shape")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ffprobeDocument{}, errors.New("ffprobe JSON contains trailing values")
	}
	return ffprobeDocument{streams: streams, format: format}, nil
}

func normalizeFFprobeDocument(document ffprobeDocument, declared mediaType, filename string, sizeBytes int64) (mediaInspection, error) {
	var videoStreams, audioStreams []map[string]any
	for _, stream := range document.streams {
		switch ffprobeString(stream["codec_type"]) {
		case "video":
			videoStreams = append(videoStreams, stream)
		case "audio":
			audioStreams = append(audioStreams, stream)
		}
	}
	formatName := ffprobeString(document.format["format_name"])
	if formatName == "" {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	formatNames := strings.Split(formatName, ",")
	if declared == mediaVideo {
		if len(videoStreams) == 0 {
			return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
		}
		if !isFFprobeMP4(formatNames, document.format) {
			return mediaInspection{}, imageInspectionFailure("UNSUPPORTED_MEDIA", false, nil)
		}
		video := videoStreams[0]
		if ffprobeString(video["codec_name"]) != "h264" {
			return mediaInspection{}, imageInspectionFailure("UNSUPPORTED_MEDIA", false, nil)
		}
		for _, stream := range audioStreams {
			if ffprobeString(stream["codec_name"]) != "aac" {
				return mediaInspection{}, imageInspectionFailure("UNSUPPORTED_MEDIA", false, nil)
			}
		}
		width, height, ok := ffprobeDimensions(video)
		if !ok {
			return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
		}
		if rotation, found, valid := ffprobeRotation(video); !valid {
			return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
		} else if found && int(math.Abs(float64(rotation)))%180 == 90 {
			width, height = height, width
		}
		if width > maxVideoSide || height > maxVideoSide || int64(width)*int64(height) > maxVideoPixels {
			return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
		}
		duration, ok := ffprobeDuration(document.format, video)
		if !ok {
			return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
		}
		if duration > maxVideoDurationMS {
			return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
		}
		if filename != "" && !extensionIs(filename, ".mp4") {
			return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
		}
		result := mediaInspection{mediaType: mediaVideo, mediaFormat: mediaMP4, sizeBytes: sizeBytes, width: width, height: height, durationMS: duration, videoCodec: "H264"}
		if len(audioStreams) > 0 {
			result.audioCodec = "AAC"
		}
		return result, nil
	}
	if len(videoStreams) > 0 {
		return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
	}
	if len(audioStreams) != 1 {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	audio := audioStreams[0]
	codec := ffprobeString(audio["codec_name"])
	format := mediaFormatInvalid
	audioCodec := ""
	if codec == "mp3" && includes(formatNames, "mp3") {
		format, audioCodec = mediaMP3, "MP3 Layer III"
	} else if codec == "aac" && isFFprobeM4A(formatNames, document.format) {
		format, audioCodec = mediaM4A, "AAC"
	} else if strings.HasPrefix(codec, "pcm_") && includes(formatNames, "wav") {
		format, audioCodec = mediaWAV, "PCM"
	} else {
		return mediaInspection{}, imageInspectionFailure("UNSUPPORTED_MEDIA", false, nil)
	}
	duration, ok := ffprobeDuration(document.format, audio)
	if !ok {
		return mediaInspection{}, imageInspectionFailure("INVALID_MEDIA", false, nil)
	}
	if duration > maxAudioDurationMS {
		return mediaInspection{}, imageInspectionFailure("MEDIA_LIMIT_EXCEEDED", false, nil)
	}
	if filename != "" && !audioExtensionMatches(filename, format) {
		return mediaInspection{}, imageInspectionFailure("MEDIA_TYPE_MISMATCH", false, nil)
	}
	return mediaInspection{mediaType: mediaAudio, mediaFormat: format, sizeBytes: sizeBytes, durationMS: duration, audioCodec: audioCodec}, nil
}

func ffprobeString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(text))
}

func ffprobeNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case float64:
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	case string:
		parsed, err := strconv.ParseFloat(number, 64)
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return 0, false
	}
}

func ffprobeDimensions(stream map[string]any) (int, int, bool) {
	width, widthOK := ffprobeNumber(stream["width"])
	height, heightOK := ffprobeNumber(stream["height"])
	if !widthOK || !heightOK || width <= 0 || height <= 0 || math.Trunc(width) != width || math.Trunc(height) != height || width > maxJSONSafeInteger || height > maxJSONSafeInteger {
		return 0, 0, false
	}
	return int(width), int(height), true
}

func ffprobeRotation(stream map[string]any) (int, bool, bool) {
	if rotation, ok := ffprobeNumber(stream["rotation"]); ok {
		return integerRotation(rotation)
	}
	if tags, ok := stream["tags"].(map[string]any); ok {
		if rotation, found := ffprobeNumber(tags["rotate"]); found {
			return integerRotation(rotation)
		}
	}
	if sideData, ok := stream["side_data_list"].([]any); ok {
		for _, item := range sideData {
			if fields, ok := item.(map[string]any); ok {
				if rotation, found := ffprobeNumber(fields["rotation"]); found {
					return integerRotation(rotation)
				}
			}
		}
	}
	return 0, false, true
}

func integerRotation(rotation float64) (int, bool, bool) {
	if math.Trunc(rotation) != rotation || math.Abs(rotation) > maxJSONSafeInteger {
		return 0, true, false
	}
	return int(rotation), true, true
}

func ffprobeDuration(format, stream map[string]any) (int64, bool) {
	seconds, ok := ffprobeNumber(format["duration"])
	if !ok {
		seconds, ok = ffprobeNumber(stream["duration"])
	}
	if !ok || seconds <= 0 || seconds*1000 > maxJSONSafeInteger {
		return 0, false
	}
	duration := int64(math.Ceil(seconds * 1000))
	return duration, duration > 0
}

func isFFprobeMP4(names []string, format map[string]any) bool {
	if !includes(names, "mp4") {
		return false
	}
	brand := ffprobeString(asObject(format["tags"])["major_brand"])
	return brand == "" || includes([]string{"isom", "iso2", "mp41", "mp42", "avc1"}, brand)
}

func isFFprobeM4A(names []string, format map[string]any) bool {
	if !includes(names, "m4a") && !includes(names, "mp4") {
		return false
	}
	brand := ffprobeString(asObject(format["tags"])["major_brand"])
	return brand == "" || includes([]string{"isom", "iso2", "mp41", "mp42", "m4a", "m4b", "m4p"}, brand)
}

func asObject(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func includes(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func extensionIs(filename, extension string) bool {
	return strings.EqualFold(filepath.Ext(filename), extension)
}

func audioExtensionMatches(filename string, format mediaFormat) bool {
	extension := strings.ToLower(filepath.Ext(filename))
	switch format {
	case mediaMP3:
		return extension == ".mp3"
	case mediaM4A:
		return extension == ".m4a" || extension == ".mp4"
	case mediaWAV:
		return extension == ".wav"
	default:
		return false
	}
}
