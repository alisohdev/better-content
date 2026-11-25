package assets

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

func TestBeginUploadCommitsAssetQuotaAndExpirationJobBeforeCapability(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	storage := &admissionStorageFake{}
	now := time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)
	service := newService(database, storage, func() time.Time { return now }, nil)

	mime := "application/x-untrusted-hint"
	result, err := service.BeginUpload(context.Background(), assetSubject(userID), UploadInput{
		WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "  preview ", OriginalFilename: "photo.PNG",
		ExpectedSizeBytes: 123, BrowserMIMEType: &mime,
	})
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	if result.Status != "PENDING" || result.SourceType != "UPLOAD" || result.MediaType != "IMAGE" || result.ExpectedSize != 123 {
		t.Fatalf("BeginUpload result = %+v", result)
	}
	if storage.key != "staging/"+storage.keyID || storage.expiresAt != now.Add(uploadCapabilityLifetime) || storage.contentLength != 123 {
		t.Fatalf("issued capability inputs = key:%q expires:%v size:%d", storage.key, storage.expiresAt, storage.contentLength)
	}
	var status, sourceType, displayName, filename, declaredMIME, stagingKey string
	var declaredSize int64
	if err := database.QueryRow(`SELECT status, source_type, display_name, original_filename, declared_byte_size, declared_mime_type, staging_key FROM assets WHERE id = $1::uuid`, result.AssetID).Scan(&status, &sourceType, &displayName, &filename, &declaredSize, &declaredMIME, &stagingKey); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" || sourceType != "UPLOAD" || displayName != "preview" || filename != "photo.PNG" || declaredSize != 123 || declaredMIME != mime || stagingKey != storage.key {
		t.Fatalf("persisted upload = %s %s %q %q %d %q %q", status, sourceType, displayName, filename, declaredSize, declaredMIME, stagingKey)
	}
	var admissionCount, expirationJobs int
	if err := database.QueryRow(`SELECT count(*) FROM asset_admission_events WHERE workspace_id = $1::uuid AND user_id = $2`, workspaceID, userID).Scan(&admissionCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM asset_jobs WHERE dedupe_key = $1 AND type = 'EXPIRE_UPLOAD' AND status = 'PENDING' AND scheduled_at = $2`, "EXPIRE_UPLOAD:"+result.AssetID, now.Add(uploadSessionLifetime)).Scan(&expirationJobs); err != nil {
		t.Fatal(err)
	}
	if admissionCount != 1 || expirationJobs != 1 {
		t.Fatalf("admission evidence count=%d expiration job count=%d", admissionCount, expirationJobs)
	}
}

func TestBeginUploadEnforcesOwnerAndAdmissionLimitsBeforePersistence(t *testing.T) {
	database := openAssetTestDatabase(t)
	_, workspaceID := createAssetWorkspace(t, database)
	service := newService(database, &admissionStorageFake{}, time.Now, nil)
	input := UploadInput{WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "image", OriginalFilename: "image.png", ExpectedSizeBytes: 10}
	if _, err := service.BeginUpload(context.Background(), assetSubject(uuid.NewString()), input); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("non-member error = %v, want forbidden", err)
	}
	if _, err := service.BeginUpload(context.Background(), assetSubject(""), input); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unauthenticated error = %v, want unauthenticated", err)
	}
	var assetCount int
	if err := database.QueryRow(`SELECT count(*) FROM assets WHERE workspace_id = $1::uuid`, workspaceID).Scan(&assetCount); err != nil {
		t.Fatal(err)
	}
	if assetCount != 0 {
		t.Fatalf("rejected admission created %d assets", assetCount)
	}
}

func TestBeginUploadEnforcesUserQuotaAndMediaByteLimits(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	now := time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)
	for index := 0; index < maxUserAssetAdmissions; index++ {
		if _, err := database.Exec(`INSERT INTO asset_admission_events (workspace_id, user_id, created_at) VALUES ($1::uuid, $2, $3)`, workspaceID, userID, now.Add(-time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	service := newService(database, &admissionStorageFake{}, func() time.Time { return now }, nil)
	input := UploadInput{WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "image", OriginalFilename: "image.png", ExpectedSizeBytes: 10}
	if _, err := service.BeginUpload(context.Background(), assetSubject(userID), input); !errors.Is(err, ErrUploadLimited) {
		t.Fatalf("quota error = %v, want rate limit", err)
	}
	input.ExpectedSizeBytes = maxImageBytes + 1
	if _, err := service.BeginUpload(context.Background(), assetSubject(userID), input); !errors.Is(err, ErrUploadInvalid) {
		t.Fatalf("oversized upload error = %v, want invalid upload", err)
	}
}

func TestRefreshAndFinalizeUploadUseCapabilitiesAndIdempotentDurableWorkflow(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	storage := &admissionStorageFake{}
	now := time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)
	service := newService(database, storage, func() time.Time { return now }, nil)
	subject := assetSubject(userID)
	created, err := service.BeginUpload(context.Background(), subject, UploadInput{
		WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "Upload", OriginalFilename: "upload.jpg", ExpectedSizeBytes: 32,
	})
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	refreshed, err := service.RefreshUploadCapability(context.Background(), subject, workspaceID, created.AssetID)
	if err != nil || refreshed.ContentLength != 32 || refreshed.URL == "" {
		t.Fatalf("RefreshUploadCapability = %+v, err=%v", refreshed, err)
	}
	storage.metadata = storedObjectMetadata{sizeBytes: 32}
	storage.metadataFound = true
	finalized, err := service.FinalizeUpload(context.Background(), subject, workspaceID, created.AssetID)
	if err != nil || finalized.AssetID != created.AssetID || !finalized.WorkflowEstablished {
		t.Fatalf("FinalizeUpload = %+v, err=%v", finalized, err)
	}
	storage.metadataErr = ErrStorageUnavailable
	repeated, err := service.FinalizeUpload(context.Background(), subject, workspaceID, created.AssetID)
	if err != nil || repeated != finalized {
		t.Fatalf("repeated FinalizeUpload = %+v, err=%v; want %+v", repeated, err, finalized)
	}
	if _, err := service.RefreshUploadCapability(context.Background(), subject, workspaceID, created.AssetID); !errors.Is(err, ErrUploadConflict) {
		t.Fatalf("refresh after finalization error = %v, want conflict", err)
	}
	var processJobs int
	if err := database.QueryRow(`SELECT count(*) FROM asset_jobs WHERE dedupe_key = $1 AND type = 'PROCESS_UPLOAD'`, "PROCESS_UPLOAD:"+created.AssetID).Scan(&processJobs); err != nil {
		t.Fatal(err)
	}
	if processJobs != 1 {
		t.Fatalf("PROCESS_UPLOAD job count = %d, want exactly one", processJobs)
	}
}

func TestFinalizeUploadRejectsMissingOrSizeMismatchedStagingObject(t *testing.T) {
	for _, test := range []struct {
		name          string
		metadata      storedObjectMetadata
		metadataFound bool
	}{
		{name: "missing object"},
		{name: "size mismatch", metadata: storedObjectMetadata{sizeBytes: 9}, metadataFound: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openAssetTestDatabase(t)
			userID, workspaceID := createAssetWorkspace(t, database)
			storage := &admissionStorageFake{metadata: test.metadata, metadataFound: test.metadataFound}
			service := newService(database, storage, time.Now, nil)
			subject := assetSubject(userID)
			created, err := service.BeginUpload(context.Background(), subject, UploadInput{
				WorkspaceID: workspaceID, MediaType: "IMAGE", DisplayName: "Upload", OriginalFilename: "upload.jpg", ExpectedSizeBytes: 32,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.FinalizeUpload(context.Background(), subject, workspaceID, created.AssetID); !errors.Is(err, ErrUploadInvalid) {
				t.Fatalf("FinalizeUpload error = %v, want invalid upload", err)
			}
		})
	}
}

func createAssetWorkspace(t *testing.T, database *sql.DB) (string, string) {
	t.Helper()
	userID, workspaceID := uuid.NewString(), uuid.NewString()
	if _, err := database.Exec(`INSERT INTO "user" (id, name, email) VALUES ($1, 'Asset admission test', $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO workspaces (id, name) VALUES ($1::uuid, 'Asset admission workspace')`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1::uuid, $2, 'owner')`, workspaceID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM asset_jobs WHERE dedupe_key LIKE 'EXPIRE_UPLOAD:%' AND payload->>'assetId' IN (SELECT id::text FROM assets WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = database.Exec(`DELETE FROM assets WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.Exec(`DELETE FROM asset_admission_events WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.Exec(`DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		_, _ = database.Exec(`DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID, workspaceID
}

type assetSubject string

func (s assetSubject) AuthenticatedUserID() (string, bool) { return string(s), s != "" }

type admissionStorageFake struct {
	key           string
	keyID         string
	expiresAt     time.Time
	contentLength int64
	metadata      storedObjectMetadata
	metadataFound bool
	metadataErr   error
}

func (f *admissionStorageFake) listManagedObjects(context.Context, storageNamespace, string, int) (managedStorageObjectPage, error) {
	return managedStorageObjectPage{}, errors.New("unexpected storage call")
}
func (f *admissionStorageFake) getObjectMetadata(context.Context, string) (storedObjectMetadata, bool, error) {
	return f.metadata, f.metadataFound, f.metadataErr
}
func (f *admissionStorageFake) openStagingRead(context.Context, stagingKey) (io.ReadCloser, error) {
	return nil, errors.New("unexpected storage call")
}
func (f *admissionStorageFake) putStagingFromStream(context.Context, stagingKey, io.Reader) error {
	return errors.New("unexpected storage call")
}
func (f *admissionStorageFake) putPermanentFromStream(context.Context, permanentKey, io.Reader) error {
	return errors.New("unexpected storage call")
}
func (f *admissionStorageFake) openPermanentRead(context.Context, permanentKey) (io.ReadCloser, error) {
	return nil, errors.New("unexpected storage call")
}
func (f *admissionStorageFake) copyStagingToPermanent(context.Context, stagingKey, permanentKey) error {
	return errors.New("unexpected storage call")
}
func (f *admissionStorageFake) deleteStagingObject(context.Context, stagingKey) error {
	return errors.New("unexpected storage call")
}
func (f *admissionStorageFake) deletePermanentObject(context.Context, permanentKey) error {
	return errors.New("unexpected storage call")
}
func (f *admissionStorageFake) createStagingPutCapability(key stagingKey, expiresAt time.Time, contentLength int64) (string, error) {
	f.key, f.keyID, f.expiresAt, f.contentLength = key.String(), key.String()[len("staging/"):], expiresAt, contentLength
	return "http://localhost/upload?signature=opaque", nil
}
func (f *admissionStorageFake) createPrivateReadCapability(context.Context, permanentKey, time.Time, privateReadOptions) (string, error) {
	return "", errors.New("unexpected storage call")
}
