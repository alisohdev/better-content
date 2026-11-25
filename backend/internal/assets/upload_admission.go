package assets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

const (
	uploadCapabilityLifetime = 4 * time.Hour
	uploadSessionLifetime    = 24 * time.Hour
	maxUserAssetAdmissions   = 30
	maxWorkspaceAdmissions   = 60
	maxActiveAssetWorkflows  = 5
)

var (
	ErrUnauthenticated    = errors.New("authenticated identity is required")
	ErrUploadInvalid      = errors.New("Asset upload request is invalid")
	ErrUploadLimited      = errors.New("Asset upload admission is temporarily limited")
	ErrStorageUnavailable = errors.New("managed storage is temporarily unavailable")
	ErrUploadConflict     = errors.New("Asset upload workflow conflicts with current state")
	ErrAssetNotFound      = errors.New("Asset was not found")
)

type UploadInput struct {
	WorkspaceID       string
	MediaType         string
	DisplayName       string
	OriginalFilename  string
	ExpectedSizeBytes int64
	BrowserMIMEType   *string
}

type UploadCapability struct {
	URL           string    `json:"url"`
	ExpiresAt     time.Time `json:"expiresAt"`
	ContentLength int64     `json:"contentLength"`
}

type BeginUploadResult struct {
	AssetID      string           `json:"assetId"`
	Status       string           `json:"status"`
	SourceType   string           `json:"sourceType"`
	MediaType    string           `json:"mediaType"`
	ExpectedSize int64            `json:"expectedUploadSizeBytes"`
	Capability   UploadCapability `json:"capability"`
}

type FinalizeUploadResult struct {
	AssetID             string `json:"assetId"`
	Status              string `json:"status"`
	WorkflowEstablished bool   `json:"workflowEstablished"`
}

type Service struct {
	database *sql.DB
	storage  assetStorage
	clock    func() time.Time
	logger   *slog.Logger
}

func newService(database *sql.DB, storage assetStorage, clock func() time.Time, logger *slog.Logger) *Service {
	if clock == nil {
		clock = time.Now
	}
	return &Service{database: database, storage: storage, clock: clock, logger: logger}
}

// NewService binds the development filesystem adapter to its database-backed
// application service without exposing storage keys or the adapter type.
func (s *DevelopmentFilesystemStorage) NewService(database *sql.DB, clock func() time.Time, logger *slog.Logger) *Service {
	return newService(database, s.storage, clock, logger)
}

func (s *Service) BeginUpload(ctx context.Context, subject workspace.MembershipSubject, input UploadInput) (BeginUploadResult, error) {
	userID, authenticated := authenticatedUserID(subject)
	if !authenticated || userID == "" {
		return BeginUploadResult{}, ErrUnauthenticated
	}
	workspaceID, err := workspace.ParseID(input.WorkspaceID)
	if err != nil {
		return BeginUploadResult{}, ErrUploadInvalid
	}
	media, err := parseMediaType(input.MediaType)
	if err != nil {
		return BeginUploadResult{}, ErrUploadInvalid
	}
	validated, err := normalizeUploadInput(uploadInput{
		mediaType:         media,
		displayName:       input.DisplayName,
		originalFilename:  input.OriginalFilename,
		expectedSizeBytes: input.ExpectedSizeBytes,
		browserMIMEType:   input.BrowserMIMEType,
	})
	if err != nil {
		return BeginUploadResult{}, ErrUploadInvalid
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return BeginUploadResult{}, err
	}

	now := s.clock().UTC()
	assetID := uuid.NewString()
	stagingKey, err := newStagingKey()
	if err != nil {
		return BeginUploadResult{}, errors.New("could not allocate upload storage key")
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return BeginUploadResult{}, errors.New("could not begin Asset upload admission")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return BeginUploadResult{}, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return BeginUploadResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID); err != nil {
		return BeginUploadResult{}, errors.New("could not serialize Asset admission")
	}
	since := now.Add(-time.Hour)
	var userEvents, workspaceEvents, activeWorkflows int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM asset_admission_events WHERE user_id = $1 AND created_at >= $2`, userID, since).Scan(&userEvents); err != nil {
		return BeginUploadResult{}, errors.New("could not read user Asset admission quota")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM asset_admission_events WHERE workspace_id = $1::uuid AND created_at >= $2`, workspaceID.String(), since).Scan(&workspaceEvents); err != nil {
		return BeginUploadResult{}, errors.New("could not read Workspace Asset admission quota")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE workspace_id = $1::uuid AND status IN ('PENDING', 'PROCESSING')`, workspaceID.String()).Scan(&activeWorkflows); err != nil {
		return BeginUploadResult{}, errors.New("could not read active Asset workflows")
	}
	if userEvents >= maxUserAssetAdmissions || workspaceEvents >= maxWorkspaceAdmissions || activeWorkflows >= maxActiveAssetWorkflows {
		return BeginUploadResult{}, ErrUploadLimited
	}
	var declaredMIME any
	if validated.browserMIMEType != nil {
		declaredMIME = *validated.browserMIMEType
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO assets (
			id, workspace_id, created_by_user_id, media_type, source_type, status,
			display_name, original_filename, declared_byte_size, declared_mime_type,
			staging_key, created_at, updated_at
		) VALUES ($1::uuid, $2::uuid, $3, $4, 'UPLOAD', 'PENDING', $5, $6, $7, $8, $9, $10, $10)
	`, assetID, workspaceID.String(), userID, media.String(), string(validated.displayName), string(validated.originalFilename), validated.expectedSize, declaredMIME, stagingKey.String(), now); err != nil {
		return BeginUploadResult{}, errors.New("could not create Asset upload")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO asset_admission_events (workspace_id, user_id, created_at) VALUES ($1::uuid, $2, $3)`, workspaceID.String(), userID, now); err != nil {
		return BeginUploadResult{}, errors.New("could not record Asset admission")
	}
	if err := createDurableAssetJob(ctx, tx, "EXPIRE_UPLOAD", assetID, now.Add(uploadSessionLifetime), now); err != nil {
		return BeginUploadResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BeginUploadResult{}, errors.New("could not commit Asset upload admission")
	}

	expiresAt := s.clock().UTC().Add(uploadCapabilityLifetime)
	capabilityURL, err := s.storage.createStagingPutCapability(stagingKey, expiresAt, validated.expectedSize)
	if err != nil {
		return BeginUploadResult{}, ErrStorageUnavailable
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "assets.upload.begun", "module", "assets", "operation", "beginUpload", "entityId", assetID, "userId", userID, "workspaceId", workspaceID.String())
	}
	return BeginUploadResult{
		AssetID: assetID, Status: "PENDING", SourceType: "UPLOAD", MediaType: media.String(),
		ExpectedSize: validated.expectedSize,
		Capability:   UploadCapability{URL: capabilityURL, ExpiresAt: expiresAt, ContentLength: validated.expectedSize},
	}, nil
}

func lockWorkspace(ctx context.Context, tx *sql.Tx, workspaceID workspace.WorkspaceID) error {
	var lockedID string
	err := tx.QueryRowContext(ctx, `SELECT id::text FROM workspaces WHERE id = $1::uuid FOR UPDATE`, workspaceID.String()).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.ErrForbidden
	}
	if err != nil {
		return errors.New("could not lock Asset Workspace")
	}
	return nil
}

func createDurableAssetJob(ctx context.Context, tx *sql.Tx, kind, assetID string, scheduledAt, now time.Time) error {
	payload, err := json.Marshal(struct {
		AssetID string `json:"assetId"`
	}{AssetID: assetID})
	if err != nil {
		return errors.New("could not encode Asset workflow")
	}
	dedupeKey := fmt.Sprintf("%s:%s", kind, assetID)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO asset_jobs (type, dedupe_key, payload, status, scheduled_at, attempts, max_attempts, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, 'PENDING', $4, 0, 5, $5, $5)
		ON CONFLICT (dedupe_key) DO NOTHING
	`, kind, dedupeKey, payload, scheduledAt, now); err != nil {
		return errors.New("could not establish durable Asset workflow")
	}
	var jobID string
	if err := tx.QueryRowContext(ctx, `SELECT id::text FROM asset_jobs WHERE dedupe_key = $1`, dedupeKey).Scan(&jobID); err != nil || jobID == "" {
		return errors.New("Asset workflow was not persisted")
	}
	return nil
}
