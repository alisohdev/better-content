package assets

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCleanupDeletedAssetRemovesObjectsBeforeMetadataAndIsIdempotent(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	staging, err := newStagingKey()
	if err != nil {
		t.Fatal(err)
	}
	var permanent string
	if err := database.QueryRow(`SELECT permanent_key FROM assets WHERE id = $1::uuid`, assetID).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE assets SET status = 'DELETING', deleting_from_ready = 1, staging_key = $2 WHERE id = $1::uuid`, assetID, staging.String()); err != nil {
		t.Fatal(err)
	}
	storage := &deletionCleanupStorageFake{metadataFound: true}
	service := newService(database, storage, time.Now, nil)
	if err := service.CleanupDeletedAsset(context.Background(), assetID); err != nil {
		t.Fatalf("CleanupDeletedAsset: %v", err)
	}
	if len(storage.deletedStaging) != 1 || storage.deletedStaging[0] != staging.String() || len(storage.deletedPermanent) != 1 || storage.deletedPermanent[0] != permanent {
		t.Fatalf("deleted storage keys staging=%v permanent=%v", storage.deletedStaging, storage.deletedPermanent)
	}
	var count int
	if err := database.QueryRow(`SELECT count(*) FROM assets WHERE id = $1::uuid`, assetID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Asset metadata count=%d err=%v", count, err)
	}
	if err := service.CleanupDeletedAsset(context.Background(), assetID); err != nil {
		t.Fatalf("repeat CleanupDeletedAsset: %v", err)
	}
	if len(storage.deletedStaging) != 1 || len(storage.deletedPermanent) != 1 {
		t.Fatalf("repeat cleanup issued additional storage deletes: staging=%v permanent=%v", storage.deletedStaging, storage.deletedPermanent)
	}
}

func TestCleanupDeletedAssetChecksReferencesBeforeRemovingObjects(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	insertAssetContentReferenceFixture(t, database, userID, workspaceID, assetID)
	if _, err := database.Exec(`UPDATE assets SET status = 'DELETING', deleting_from_ready = 1 WHERE id = $1::uuid`, assetID); err != nil {
		t.Fatal(err)
	}
	storage := &deletionCleanupStorageFake{metadataFound: true}
	service := newService(database, storage, time.Now, nil)
	if err := service.CleanupDeletedAsset(context.Background(), assetID); !errors.Is(err, ErrAssetInUse) {
		t.Fatalf("cleanup reference error = %v, want ErrAssetInUse", err)
	}
	if len(storage.deletedStaging) != 0 || len(storage.deletedPermanent) != 0 {
		t.Fatalf("referenced Asset objects were removed: staging=%v permanent=%v", storage.deletedStaging, storage.deletedPermanent)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM assets WHERE id = $1::uuid`, assetID).Scan(&status); err != nil || status != "DELETING" {
		t.Fatalf("referenced Asset status=%q err=%v", status, err)
	}
}

type deletionCleanupStorageFake struct {
	assetStorage
	metadataFound    bool
	deletedStaging   []string
	deletedPermanent []string
}

func (storage *deletionCleanupStorageFake) getObjectMetadata(context.Context, string) (storedObjectMetadata, bool, error) {
	return storedObjectMetadata{sizeBytes: 1}, storage.metadataFound, nil
}

func (storage *deletionCleanupStorageFake) deleteStagingObject(_ context.Context, key stagingKey) error {
	storage.deletedStaging = append(storage.deletedStaging, key.String())
	return nil
}

func (storage *deletionCleanupStorageFake) deletePermanentObject(_ context.Context, key permanentKey) error {
	storage.deletedPermanent = append(storage.deletedPermanent, key.String())
	return nil
}

var _ assetStorage = (*deletionCleanupStorageFake)(nil)
