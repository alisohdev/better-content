package assets

import (
	"context"
	"errors"
	"testing"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

func TestCreateExternalURLAssetStoresNormalizedSourceAndQueuesIngestion(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	now := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	service := newService(database, nil, func() time.Time { return now }, nil)
	result, err := service.CreateExternalURLAsset(context.Background(), assetSubject(userID), ExternalURLInput{
		WorkspaceID: workspaceID,
		MediaType:   "IMAGE",
		DisplayName: "  campaign reference  ",
		SourceURL:   "https://BÜCHER.example:443/media?id=1#preview",
	})
	if err != nil {
		t.Fatalf("CreateExternalURLAsset: %v", err)
	}
	if result.Status != "PENDING" || result.SourceType != "EXTERNAL_URL" || result.MediaType != "IMAGE" || result.SourceHost != "xn--bcher-kva.example" {
		t.Fatalf("external Asset result = %+v", result)
	}
	var sourceURL, sourceHost, status, sourceType, displayName, mediaType, stagingKey string
	if err := database.QueryRow(`SELECT source_url, source_host, status, source_type, display_name, media_type, staging_key FROM assets WHERE id = $1::uuid`, result.AssetID).Scan(&sourceURL, &sourceHost, &status, &sourceType, &displayName, &mediaType, &stagingKey); err != nil {
		t.Fatal(err)
	}
	if sourceURL != "https://xn--bcher-kva.example/media?id=1" || sourceHost != result.SourceHost || status != "PENDING" || sourceType != "EXTERNAL_URL" || displayName != "campaign reference" || mediaType != "IMAGE" || stagingKey == "" {
		t.Fatalf("persisted external Asset = %q %q %q %q %q %q %q", sourceURL, sourceHost, status, sourceType, displayName, mediaType, stagingKey)
	}
	var jobType, jobStatus, dedupeKey string
	if err := database.QueryRow(`SELECT type, status, dedupe_key FROM asset_jobs WHERE payload->>'assetId' = $1`, result.AssetID).Scan(&jobType, &jobStatus, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if jobType != "INGEST_EXTERNAL_URL" || jobStatus != "PENDING" || dedupeKey != "INGEST_EXTERNAL_URL:"+result.AssetID {
		t.Fatalf("external Asset job = %s %s %s", jobType, jobStatus, dedupeKey)
	}
	var admissions int
	if err := database.QueryRow(`SELECT count(*) FROM asset_admission_events WHERE workspace_id = $1::uuid AND user_id = $2 AND created_at = $3`, workspaceID, userID, now).Scan(&admissions); err != nil || admissions != 1 {
		t.Fatalf("external Asset admission count = %d, err=%v", admissions, err)
	}
	if _, err := service.CreateExternalURLAsset(context.Background(), assetSubject(uuid.NewString()), ExternalURLInput{
		WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "forbidden", SourceURL: "https://example.com/media",
	}); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("non-owner external Asset error = %v, want forbidden", err)
	}
}

func TestCreateExternalURLAssetRejectsUnsafeSourcesBeforePersistence(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	service := newService(database, nil, time.Now, nil)
	for _, sourceURL := range []string{"http://example.com/media", "https://user@example.com/media", "https://example.com:444/media", "data:text/plain,media"} {
		_, err := service.CreateExternalURLAsset(context.Background(), assetSubject(userID), ExternalURLInput{
			WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "unsafe source", SourceURL: sourceURL,
		})
		if !errors.Is(err, ErrExternalURLInvalid) {
			t.Errorf("source %q error = %v, want invalid URL", sourceURL, err)
		}
	}
	var assetsCount, jobsCount, admissions int
	if err := database.QueryRow(`SELECT count(*) FROM assets WHERE workspace_id = $1::uuid`, workspaceID).Scan(&assetsCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM asset_jobs WHERE payload->>'assetId' IN (SELECT id::text FROM assets WHERE workspace_id = $1::uuid)`, workspaceID).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM asset_admission_events WHERE workspace_id = $1::uuid`, workspaceID).Scan(&admissions); err != nil {
		t.Fatal(err)
	}
	if assetsCount != 0 || jobsCount != 0 || admissions != 0 {
		t.Fatalf("unsafe source persisted %d assets, %d jobs, and %d admission events", assetsCount, jobsCount, admissions)
	}
}
