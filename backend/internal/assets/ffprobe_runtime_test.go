package assets

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Compare real output with the legacy Node adapter's recorded results on the
// checksum-verified Debian runtime, using the same corpus bytes.
func TestPinnedFFprobeMatchesLegacyRuntime(t *testing.T) {
	executable := os.Getenv("ASSET_FFPROBE_PARITY_PATH")
	if executable == "" {
		t.Skip("ASSET_FFPROBE_PARITY_PATH is not configured")
	}
	const version = "7.1.5-0+deb13u1"
	inspector := newMediaInspector(executable, version, nil)
	ctx := context.Background()
	if err := inspector.verifyRuntime(ctx); err != nil {
		t.Fatal(err)
	}

	var baseline avBaseline
	readParityJSON(t, filepath.Join("testdata", "av", "baseline.json"), &baseline)
	var legacy struct {
		Results []struct {
			Corpus  string
			Fixture string
			Actual  struct {
				avBaselineResult
				FailureCode string
			}
		}
	}
	readParityJSON(t, filepath.Join("..", "..", "..", ".scratch", "b-spike", "evidence", "node-media.json"), &legacy)
	for _, fixture := range baseline.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			for _, comparison := range legacy.Results {
				if comparison.Corpus != "av" || comparison.Fixture != fixture.Name {
					continue
				}
				path, err := filepath.Abs(filepath.Join("testdata", "av", fixture.Name+".bin"))
				if err != nil {
					t.Fatal(err)
				}
				declared, err := parseMediaType(fixture.DeclaredMedia)
				if err != nil {
					t.Fatal(err)
				}
				result, err := inspector.inspect(ctx, path, declared, fixture.Filename, fixture.SizeBytes)
				if comparison.Actual.FailureCode != "" {
					var failure *mediaInspectionError
					if !errors.As(err, &failure) || failure.failureCode != comparison.Actual.FailureCode {
						t.Fatalf("inspection failure = %v, want %s", err, comparison.Actual.FailureCode)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				normalized := avBaselineResult{
					MediaType: result.mediaType.String(), MediaFormat: result.mediaFormat.String(),
					SizeBytes: result.sizeBytes, Width: result.width, Height: result.height, DurationMS: result.durationMS,
					AudioCodec: result.audioCodec, VideoCodec: result.videoCodec,
				}
				if normalized != comparison.Actual.avBaselineResult {
					t.Fatalf("Go result = %+v, legacy runtime gave %+v", normalized, comparison.Actual.avBaselineResult)
				}
				return
			}
			t.Fatal("fixture has no legacy runtime evidence")
		})
	}
}

func readParityJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}
