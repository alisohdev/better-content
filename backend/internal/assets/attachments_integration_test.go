package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/platform/postgres"
	"github.com/google/uuid"
)

func TestValidateAttachmentsEnforcesWorkspaceReadinessAndMediaCompatibility(t *testing.T) {
	database := openAssetTestDatabase(t)
	ctx := context.Background()
	userID, workspaceID := uuid.NewString(), uuid.NewString()
	if _, err := database.ExecContext(ctx, `INSERT INTO "user" (id, name, email) VALUES ($1, 'Asset test', $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO workspaces (id, name) VALUES ($1::uuid, 'Asset test workspace')`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1::uuid, $2, 'owner')`, workspaceID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM assets WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.Exec(`DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		_, _ = database.Exec(`DELETE FROM "user" WHERE id = $1`, userID)
	})

	imageID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	audioID := insertAttachmentFixture(t, database, workspaceID, userID, "AUDIO", "READY")
	pendingImageID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "PENDING")
	for _, test := range []struct {
		name      string
		assetID   string
		direction string
		workspace string
		wantErr   bool
	}{
		{name: "ready image for B-roll", assetID: imageID, direction: "BROLL_CUE", workspace: workspaceID},
		{name: "ready audio for sound cue", assetID: audioID, direction: "SOUND_CUE", workspace: workspaceID},
		{name: "media mismatch", assetID: audioID, direction: "BROLL_CUE", workspace: workspaceID, wantErr: true},
		{name: "pending Asset", assetID: pendingImageID, direction: "BROLL_CUE", workspace: workspaceID, wantErr: true},
		{name: "cross-Workspace Asset", assetID: imageID, direction: "BROLL_CUE", workspace: uuid.NewString(), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := attachmentDocument(t, test.assetID, test.direction)
			tx, err := database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = ValidateAttachments(ctx, tx, test.workspace, doc)
			if test.wantErr {
				if !errors.Is(err, ErrAttachmentInvalid) {
					t.Fatalf("ValidateAttachments error = %v, want invalid attachment", err)
				}
			} else if err != nil {
				t.Fatalf("ValidateAttachments: %v", err)
			}
		})
	}
}

func attachmentDocument(t *testing.T, assetID, directionType string) document.Document {
	t.Helper()
	blockID, directionID := uuid.NewString(), uuid.NewString()
	directionMetadata := `"searchQuery":"visual cue"`
	if directionType == "SOUND_CUE" {
		directionMetadata = `"kind":"music"`
	}
	content := fmt.Sprintf(`{"schemaVersion":4,"script":{"blocks":[{"id":%q,"type":"paragraph","text":"Line","performanceDirections":[],"editDirections":[{"id":%q,"type":%q,"description":"cue","assetId":%q,%s}]}]}}`, blockID, directionID, directionType, assetID, directionMetadata)
	parsed, err := document.ParseV4([]byte(content))
	if err != nil {
		t.Fatalf("parse fixture document: %v", err)
	}
	return parsed.Document()
}

func insertAttachmentFixture(t *testing.T, database *sql.DB, workspaceID, userID, mediaType, status string) string {
	t.Helper()
	assetID := uuid.NewString()
	permanentKey := "permanent/" + uuid.NewString()
	var originalFilename any = "fixture.png"
	var permanentKeyValue any = permanentKey
	var byteSize, detectedMIME, mediaFormat any = 1, "image/png", "PNG"
	if mediaType == "AUDIO" {
		originalFilename, detectedMIME, mediaFormat = "fixture.mp3", "audio/mpeg", "MP3"
	}
	if status != "READY" {
		permanentKeyValue, byteSize, detectedMIME, mediaFormat = nil, nil, nil, nil
	}
	_, err := database.Exec(`
		INSERT INTO assets (
			id, workspace_id, created_by_user_id, media_type, source_type, status,
			display_name, original_filename, permanent_key, byte_size,
			detected_mime_type, media_format
		) VALUES ($1::uuid, $2::uuid, $3, $4, 'UPLOAD', $5, 'Fixture', $6, $7, $8, $9, $10)
	`, assetID, workspaceID, userID, mediaType, status, originalFilename, permanentKeyValue, byteSize, detectedMIME, mediaFormat)
	if err != nil {
		t.Fatalf("insert %s Asset fixture: %v", status, err)
	}
	return assetID
}

func openAssetTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must target a database ending in _test")
	}
	if primary := os.Getenv("DATABASE_URL"); primary != "" {
		primaryURL, parseErr := url.Parse(primary)
		if parseErr != nil || strings.EqualFold(parsed.Host+parsed.Path, primaryURL.Host+primaryURL.Path) {
			t.Fatal("TEST_DATABASE_URL must not identify DATABASE_URL")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	database, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
