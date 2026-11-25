package assets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type imageBaseline struct {
	Fixtures []imageBaselineFixture `json:"fixtures"`
}

type imageBaselineFixture struct {
	Name          string               `json:"name"`
	SHA256        string               `json:"sha256"`
	SizeBytes     int64                `json:"sizeBytes"`
	DeclaredMedia string               `json:"declaredMediaType"`
	Filename      string               `json:"filename"`
	Result        *imageBaselineResult `json:"result"`
	FailureCode   string               `json:"failureCode"`
}

type imageBaselineResult struct {
	MediaType   string `json:"mediaType"`
	MediaFormat string `json:"mediaFormat"`
	SizeBytes   int64  `json:"sizeBytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

func TestImageInspectorMatchesLegacyFixtureCorpus(t *testing.T) {
	manifestBytes, err := os.ReadFile(filepath.Join("testdata", "images", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline imageBaseline
	if err := json.Unmarshal(manifestBytes, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Fixtures) == 0 {
		t.Fatal("image baseline corpus is empty")
	}
	for _, fixture := range baseline.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "images", fixture.Name+".bin"))
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
			result, inspectErr := inspectImage(context.Background(), bytes.NewReader(data), declared, fixture.Filename, fixture.SizeBytes)
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
			got := imageBaselineResult{MediaType: result.mediaType.String(), MediaFormat: result.mediaFormat.String(), SizeBytes: result.sizeBytes, Width: result.width, Height: result.height}
			if got != *fixture.Result {
				t.Fatalf("inspection result = %#v, baseline requires %#v", got, *fixture.Result)
			}
		})
	}
}
