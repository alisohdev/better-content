package assets

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestReconcileManagedObjectPageUsesAgeAndDatabaseOwnership(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	admissionStorage := &admissionStorageFake{}
	service := newService(database, admissionStorage, time.Now, nil)
	created, err := service.BeginUpload(context.Background(), assetSubject(userID), UploadInput{WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "Owned", OriginalFilename: "owned.png", ExpectedSizeBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	orphan, _ := newStagingKey()
	recent, _ := newStagingKey()
	now := time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)
	storage := &reconciliationStorageFake{page: managedStorageObjectPage{
		objects: []managedStorageObject{
			{key: orphan.String(), lastModified: now.Add(-25 * time.Hour)},
			{key: admissionStorage.key, lastModified: now.Add(-25 * time.Hour)},
			{key: recent.String(), lastModified: now.Add(-23 * time.Hour)},
		},
		nextCursor: "next-page",
	}}
	service.storage = storage
	result, err := service.ReconcileManagedObjectPage(context.Background(), stagingNamespace, "", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Retained != 2 || result.NextCursor != "next-page" || len(storage.deletedStaging) != 1 || storage.deletedStaging[0] != orphan.String() {
		t.Fatalf("managed reconciliation = %+v deleted=%v", result, storage.deletedStaging)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM assets WHERE id = $1::uuid`, created.AssetID).Scan(&status); err != nil || status != "PENDING" {
		t.Fatalf("reconciliation changed owned Asset status=%q err=%v", status, err)
	}
}

func TestReconcileReadyObjectIntegrityObservesWithoutMutating(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	storage := &reconciliationStorageFake{metadataFound: false}
	service := newService(database, storage, time.Now, nil)
	result, err := service.ReconcileReadyObjectIntegrity(context.Background(), "", 1)
	if err != nil || result.Processed != 1 || result.NextAfterID != assetID {
		t.Fatalf("integrity reconciliation = %+v err=%v", result, err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM assets WHERE id = $1::uuid`, assetID).Scan(&status); err != nil || status != "READY" {
		t.Fatalf("integrity observer mutated status=%q err=%v", status, err)
	}
}

type reconciliationStorageFake struct {
	assetStorage
	page             managedStorageObjectPage
	metadataFound    bool
	deletedStaging   []string
	deletedPermanent []string
}

func (storage *reconciliationStorageFake) listManagedObjects(context.Context, storageNamespace, string, int) (managedStorageObjectPage, error) {
	return storage.page, nil
}

func (storage *reconciliationStorageFake) getObjectMetadata(context.Context, string) (storedObjectMetadata, bool, error) {
	return storedObjectMetadata{sizeBytes: 1}, storage.metadataFound, nil
}

func (storage *reconciliationStorageFake) deleteStagingObject(_ context.Context, key stagingKey) error {
	storage.deletedStaging = append(storage.deletedStaging, key.String())
	return nil
}

func (storage *reconciliationStorageFake) deletePermanentObject(_ context.Context, key permanentKey) error {
	storage.deletedPermanent = append(storage.deletedPermanent, key.String())
	return nil
}

func (storage *reconciliationStorageFake) putStagingFromStream(context.Context, stagingKey, io.Reader) error {
	return errors.New("unexpected storage operation")
}

var _ assetStorage = (*reconciliationStorageFake)(nil)
