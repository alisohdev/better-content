package assets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFFprobeInspectorNormalizesVideoAndAudio(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("video fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(tempDir, "track.mp3")
	if err := os.WriteFile(audioPath, []byte("audio fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	videoJSON := `{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"side_data_list":[{"rotation":90}]},{"codec_type":"audio","codec_name":"aac"}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"12.001","tags":{"major_brand":"isom"}}}`
	audioJSON := `{"streams":[{"codec_type":"audio","codec_name":"mp3","duration":"1.25"}],"format":{"format_name":"mp3"}}`
	var calls [][]string
	execute := func(_ context.Context, _ string, args []string, _ time.Duration, _ int) (ffprobeResult, error) {
		calls = append(calls, append([]string(nil), args...))
		output := videoJSON
		if args[len(args)-1] == audioPath {
			output = audioJSON
		}
		return ffprobeResult{stdout: []byte(output)}, nil
	}
	inspector := newFFprobeInspector("configured-ffprobe", "8.1.2", execute)
	video, err := inspector.inspect(context.Background(), videoPath, mediaVideo, "clip.mp4", int64(len("video fixture")))
	if err != nil {
		t.Fatal(err)
	}
	if video.mediaType != mediaVideo || video.mediaFormat != mediaMP4 || video.width != 1080 || video.height != 1920 || video.durationMS != 12001 || video.videoCodec != "H264" || video.audioCodec != "AAC" {
		t.Fatalf("video inspection = %#v", video)
	}
	audio, err := inspector.inspect(context.Background(), audioPath, mediaAudio, "track.mp3", int64(len("audio fixture")))
	if err != nil {
		t.Fatal(err)
	}
	if audio.mediaType != mediaAudio || audio.mediaFormat != mediaMP3 || audio.durationMS != 1250 || audio.audioCodec != "MP3 Layer III" || audio.width != 0 {
		t.Fatalf("audio inspection = %#v", audio)
	}
	wantArgs := []string{"-v", "error", "-hide_banner", "-print_format", "json", "-show_streams", "-show_format", "-protocol_whitelist", "file", videoPath}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], wantArgs) {
		t.Fatalf("ffprobe arguments = %#v, want %#v", calls, wantArgs)
	}
}

func TestFFprobeInspectorRequiresConstrainedInputsAndStrictOutput(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sound.wav")
	if err := os.WriteFile(path, []byte("wave"), 0o600); err != nil {
		t.Fatal(err)
	}
	executions := 0
	inspector := newFFprobeInspector("configured-ffprobe", "8.1.2", func(_ context.Context, _ string, args []string, _ time.Duration, _ int) (ffprobeResult, error) {
		executions++
		if len(args) == 1 {
			return ffprobeResult{stdout: []byte("ffprobe version 8.1.2 Copyright")}, nil
		}
		return ffprobeResult{stdout: []byte(`{"streams":[],"format":{"format_name":"wav"},"extra":true}`)}, nil
	})
	if err := inspector.verifyRuntime(context.Background()); err != nil {
		t.Fatalf("matching ffprobe version rejected: %v", err)
	}
	if _, err := inspector.inspect(context.Background(), path, mediaAudio, "sound.wav", 4); err == nil || mustInspectionFailureCode(t, err) != "PROCESSING_UNAVAILABLE" {
		t.Fatalf("invalid ffprobe JSON error = %v, want PROCESSING_UNAVAILABLE", err)
	}
	if _, err := inspector.inspect(context.Background(), path, mediaImage, "sound.wav", 4); err == nil || mustInspectionFailureCode(t, err) != "MEDIA_TYPE_MISMATCH" {
		t.Fatalf("declared media error = %v, want MEDIA_TYPE_MISMATCH", err)
	}
	if _, err := inspector.inspect(context.Background(), path, mediaAudio, "sound.wav", maxAudioBytes+1); err == nil || mustInspectionFailureCode(t, err) != "MEDIA_LIMIT_EXCEEDED" {
		t.Fatalf("oversize media error = %v, want MEDIA_LIMIT_EXCEEDED", err)
	}
	if executions != 2 {
		t.Fatalf("ffprobe executions = %d, oversize/type mismatch must not invoke process", executions)
	}
}

func TestFFprobeInspectorRejectsWrongRuntimeAndOutputOverflow(t *testing.T) {
	t.Parallel()
	inspector := newFFprobeInspector("configured-ffprobe", "8.1.2", func(context.Context, string, []string, time.Duration, int) (ffprobeResult, error) {
		return ffprobeResult{stdout: []byte("ffprobe version 8.1.1 Copyright")}, nil
	})
	if err := inspector.verifyRuntime(context.Background()); err == nil || mustInspectionFailureCode(t, err) != "PROCESSING_UNAVAILABLE" {
		t.Fatalf("wrong runtime error = %v, want PROCESSING_UNAVAILABLE", err)
	}
	output := &limitedCommandOutput{limit: 4}
	if n, err := output.Write([]byte("too much output")); err != nil || n != len("too much output") || !output.exceeded || output.data.String() != "too " {
		t.Fatalf("bounded output = n:%d err:%v exceeded:%v data:%q", n, err, output.exceeded, output.data.String())
	}
	if _, err := parseFFprobeDocument([]byte(`{"streams":[],"format":{}} {}`)); err == nil {
		t.Fatal("trailing ffprobe JSON value was accepted")
	}
	if _, err := parseFFprobeDocument([]byte(`{"streams":[],"format":{}}`)); err != nil {
		t.Fatalf("valid ffprobe JSON rejected: %v", err)
	}
}

func TestNormalizeFFprobeDocumentSupportsM4AWAVAndEnforcesMediaBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		declared   mediaType
		filename   string
		streams    []map[string]any
		format     map[string]any
		wantFormat mediaFormat
		wantCode   string
	}{
		{
			name:       "M4A",
			declared:   mediaAudio,
			filename:   "voice.mp4",
			streams:    []map[string]any{{"codec_type": "audio", "codec_name": "aac"}},
			format:     map[string]any{"format_name": "mov,mp4,m4a", "duration": "1.5", "tags": map[string]any{"major_brand": "m4a "}},
			wantFormat: mediaM4A,
		},
		{
			name:       "WAV",
			declared:   mediaAudio,
			filename:   "voice.wav",
			streams:    []map[string]any{{"codec_type": "audio", "codec_name": "pcm_s16le", "duration": 1.25}},
			format:     map[string]any{"format_name": "wav", "duration": "1.25"},
			wantFormat: mediaWAV,
		},
		{
			name:     "unsupported codec",
			declared: mediaAudio,
			filename: "voice.mp3",
			streams:  []map[string]any{{"codec_type": "audio", "codec_name": "vorbis", "duration": 1.0}},
			format:   map[string]any{"format_name": "ogg", "duration": "1.0"},
			wantCode: "UNSUPPORTED_MEDIA",
		},
		{
			name:     "video duration limit",
			declared: mediaVideo,
			filename: "clip.mp4",
			streams:  []map[string]any{{"codec_type": "video", "codec_name": "h264", "width": float64(1920), "height": float64(1080), "duration": "1800.001"}},
			format:   map[string]any{"format_name": "mp4", "duration": "1800.001"},
			wantCode: "MEDIA_LIMIT_EXCEEDED",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, err := normalizeFFprobeDocument(ffprobeDocument{streams: test.streams, format: test.format}, test.declared, test.filename, 100)
			if test.wantCode != "" {
				if got := mustInspectionFailureCode(t, err); got != test.wantCode {
					t.Fatalf("failure code = %s, want %s", got, test.wantCode)
				}
				return
			}
			if err != nil || result.mediaFormat != test.wantFormat {
				t.Fatalf("result = %#v, err=%v, want format %s", result, err, test.wantFormat)
			}
		})
	}
}

func mustInspectionFailureCode(t *testing.T, err error) string {
	t.Helper()
	var inspectionErr *mediaInspectionError
	if !errors.As(err, &inspectionErr) {
		t.Fatalf("error %v is not a media inspection error", err)
	}
	return inspectionErr.failureCode
}
