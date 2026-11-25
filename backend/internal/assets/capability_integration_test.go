package assets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bettercontent/backend/internal/workspace"
)

func TestIssueReadCapabilityRequiresMembershipAndReadyState(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "AUDIO", "READY")
	now := time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC)
	storage := &capabilityStorageFake{}
	service := newService(database, storage, func() time.Time { return now }, nil)
	input := AssetReadCapabilityInput{WorkspaceID: workspaceID, AssetID: assetID, Operation: "PREVIEW"}
	capability, err := service.IssueReadCapability(context.Background(), assetSubject(userID), input)
	if err != nil {
		t.Fatal(err)
	}
	if capability.URL != "https://private.example/read" || capability.ContentType != "audio/mpeg" || capability.ContentDisposition != "inline" || !capability.RangeAllowed || !capability.ExpiresAt.Equal(now.Add(privateReadCapabilityLifetime)) {
		t.Fatalf("preview capability = %+v", capability)
	}
	if storage.key != "permanent/"+storage.keyID || storage.options.rangeAllowed != true {
		t.Fatalf("read capability inputs key=%q options=%+v", storage.key, storage.options)
	}

	input.Operation = "DOWNLOAD"
	capability, err = service.IssueReadCapability(context.Background(), assetSubject(userID), input)
	if err != nil || !strings.HasPrefix(capability.ContentDisposition, "attachment; filename=\"Fixture.mp3\"") {
		t.Fatalf("download capability=%+v err=%v", capability, err)
	}
	if _, err := service.IssueReadCapability(context.Background(), assetSubject(""), input); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unauthenticated capability error = %v", err)
	}
	if _, err := service.IssueReadCapability(context.Background(), assetSubject("another-user"), input); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("cross-Workspace capability error = %v", err)
	}
	if _, err := database.Exec(`UPDATE assets SET status = 'PROCESSING' WHERE id = $1::uuid`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.IssueReadCapability(context.Background(), assetSubject(userID), input); !errors.Is(err, ErrAssetConflict) {
		t.Fatalf("non-ready capability error = %v", err)
	}
}

func TestDownloadDispositionPreservesUnicodeAndSafeFallback(t *testing.T) {
	filename, err := safeDownloadFilename("پیش نمایش", mediaJPEG)
	if err != nil {
		t.Fatal(err)
	}
	got := attachmentContentDisposition(filename)
	if got != "attachment; filename=\"___ _____.jpg\"; filename*=UTF-8''%D9%BE%DB%8C%D8%B4%20%D9%86%D9%85%D8%A7%DB%8C%D8%B4.jpg" {
		t.Fatalf("attachment content disposition = %q", got)
	}
}

type capabilityStorageFake struct {
	assetStorage
	key     string
	keyID   string
	expires time.Time
	options privateReadOptions
}

func (storage *capabilityStorageFake) createPrivateReadCapability(_ context.Context, key permanentKey, expiresAt time.Time, options privateReadOptions) (string, error) {
	storage.key = key.String()
	storage.keyID = key.String()[len("permanent/"):]
	storage.expires = expiresAt
	storage.options = options
	return "https://private.example/read", nil
}

var _ assetStorage = (*capabilityStorageFake)(nil)
