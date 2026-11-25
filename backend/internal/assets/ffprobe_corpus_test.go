package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type avBaseline struct {
	SchemaVersion int `json:"schemaVersion"`
	Provenance    struct {
		FFprobeVersion string `json:"ffprobeVersion"`
		Provisional    bool   `json:"provisional"`
	} `json:"provenance"`
	Fixtures []avBaselineFixture `json:"fixtures"`
}

type avBaselineFixture struct {
	Name          string            `json:"name"`
	SHA256        string            `json:"sha256"`
	SizeBytes     int64             `json:"sizeBytes"`
	DeclaredMedia string            `json:"declaredMediaType"`
	Filename      string            `json:"filename"`
	ProbeExitCode *int              `json:"probeExitCode"`
	ProbeOutput   string            `json:"probeOutput"`
	Result        *avBaselineResult `json:"result"`
	FailureCode   string            `json:"failureCode"`
}

type avBaselineResult struct {
	MediaType   string `json:"mediaType"`
	MediaFormat string `json:"mediaFormat"`
	SizeBytes   int64  `json:"sizeBytes"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	DurationMS  int64  `json:"durationMs,omitempty"`
	AudioCodec  string `json:"audioCodec,omitempty"`
	VideoCodec  string `json:"videoCodec,omitempty"`
}

func TestFFprobeInspectorMatchesLegacyFixtureCorpus(t *testing.T) {
	manifestBytes, err := os.ReadFile(filepath.Join("testdata", "av", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline avBaseline
	if err := json.Unmarshal(manifestBytes, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.SchemaVersion != 1 || baseline.Provenance.FFprobeVersion == "" || len(baseline.Fixtures) == 0 {
		t.Fatal("audio/video baseline corpus has invalid provenance or no fixtures")
	}
	if baseline.Provenance.Provisional {
		t.Logf("legacy baseline uses provisional ffprobe %q", baseline.Provenance.FFprobeVersion)
	}
	for _, fixture := range baseline.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "av", fixture.Name+".bin"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			if got := hex.EncodeToString(digest[:]); got != fixture.SHA256 {
				t.Fatalf("fixture digest = %s, baseline requires %s", got, fixture.SHA256)
			}
			declared, err := parseMediaType(fixture.DeclaredMedia)
			if err != nil {
				t.Fatal(err)
			}
			var execute ffprobeExecutor
			if fixture.ProbeExitCode != nil {
				if fixture.ProbeOutput == "" {
					t.Fatal("ffprobe-invoked fixture has no captured output")
				}
				execute = func(_ context.Context, _ string, _ []string, _ time.Duration, _ int) (ffprobeResult, error) {
					return ffprobeResult{exitCode: *fixture.ProbeExitCode, stdout: []byte(fixture.ProbeOutput)}, nil
				}
			}
			inspector := newMediaInspector("fixture-ffprobe", baseline.Provenance.FFprobeVersion, execute)
			path, err := filepath.Abs(filepath.Join("testdata", "av", fixture.Name+".bin"))
			if err != nil {
				t.Fatal(err)
			}
			result, inspectErr := inspector.inspect(context.Background(), path, declared, fixture.Filename, fixture.SizeBytes)
			if fixture.FailureCode != "" {
				var failure *mediaInspectionError
				if !errors.As(inspectErr, &failure) || failure.failureCode != fixture.FailureCode {
					t.Fatalf("inspection error = %v, want failure %s", inspectErr, fixture.FailureCode)
				}
				return
			}
			if inspectErr != nil {
				t.Fatalf("inspection failed: %v", inspectErr)
			}
			if fixture.Result == nil {
				t.Fatal("fixture must define either a result or failure")
			}
			got := avBaselineResult{
				MediaType: result.mediaType.String(), MediaFormat: result.mediaFormat.String(),
				SizeBytes: result.sizeBytes, Width: result.width, Height: result.height,
				DurationMS: result.durationMS, AudioCodec: result.audioCodec, VideoCodec: result.videoCodec,
			}
			if !reflect.DeepEqual(got, *fixture.Result) {
				t.Fatalf("inspection result = %#v, baseline requires %#v", got, *fixture.Result)
			}
		})
	}
}
