package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

type uploadState struct {
	id               string
	workspaceID      string
	status           string
	sourceType       string
	mediaType        string
	createdAt        time.Time
	stagingKey       sql.NullString
	declaredByteSize sql.NullInt64
}

// RefreshUploadCapability reissues a bounded PUT capability only for an open,
// unexpired upload session whose processing workflow has not been established.
func (s *Service) RefreshUploadCapability(ctx context.Context, subject workspace.MembershipSubject, workspaceIDText, assetID string) (UploadCapability, error) {
	workspaceID, parsedAssetID, err := parseUploadMutationIDs(workspaceIDText, assetID)
	if err != nil {
		return UploadCapability{}, err
	}
	if _, authenticated := authenticatedUserID(subject); !authenticated {
		return UploadCapability{}, ErrUnauthenticated
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return UploadCapability{}, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return UploadCapability{}, errors.New("could not begin upload capability refresh")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return UploadCapability{}, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return UploadCapability{}, err
	}
	state, err := findUploadForUpdate(ctx, tx, workspaceID.String(), parsedAssetID)
	if errors.Is(err, sql.ErrNoRows) {
		return UploadCapability{}, ErrAssetNotFound
	}
	if err != nil {
		return UploadCapability{}, err
	}
	finalized, err := hasDurableAssetJob(ctx, tx, "PROCESS_UPLOAD", state.id)
	if err != nil {
		return UploadCapability{}, err
	}
	if !uploadCapabilityEligible(state, finalized, s.clock()) {
		return UploadCapability{}, ErrUploadConflict
	}
	if !state.stagingKey.Valid || !state.declaredByteSize.Valid {
		return UploadCapability{}, errors.New("upload is missing staging metadata")
	}
	key, err := parseStagingKey(state.stagingKey.String)
	if err != nil {
		return UploadCapability{}, errors.New("upload has invalid staging metadata")
	}
	if err := tx.Commit(); err != nil {
		return UploadCapability{}, errors.New("could not finish upload capability refresh")
	}
	return s.issueUploadCapability(key, state.declaredByteSize.Int64)
}

// FinalizeUpload validates staged object metadata outside SQL transactions,
// then establishes one durable processing workflow under the Asset row lock.
func (s *Service) FinalizeUpload(ctx context.Context, subject workspace.MembershipSubject, workspaceIDText, assetID string) (FinalizeUploadResult, error) {
	workspaceID, parsedAssetID, err := parseUploadMutationIDs(workspaceIDText, assetID)
	if err != nil {
		return FinalizeUploadResult{}, err
	}
	if _, authenticated := authenticatedUserID(subject); !authenticated {
		return FinalizeUploadResult{}, ErrUnauthenticated
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return FinalizeUploadResult{}, err
	}
	preliminary, finalized, err := s.lockUploadMutation(ctx, subject, workspaceID, parsedAssetID)
	if err != nil {
		return FinalizeUploadResult{}, err
	}
	if finalized {
		return FinalizeUploadResult{AssetID: preliminary.id, Status: "PENDING", WorkflowEstablished: true}, nil
	}
	if !preliminary.stagingKey.Valid || !preliminary.declaredByteSize.Valid {
		return FinalizeUploadResult{}, errors.New("upload is missing staging metadata")
	}
	key, err := parseStagingKey(preliminary.stagingKey.String)
	if err != nil {
		return FinalizeUploadResult{}, errors.New("upload has invalid staging metadata")
	}
	metadata, found, err := s.storage.getObjectMetadata(ctx, key.String())
	if err != nil {
		if errors.Is(err, errStorageNotFound) {
			return FinalizeUploadResult{}, ErrUploadInvalid
		}
		return FinalizeUploadResult{}, ErrStorageUnavailable
	}
	if !found || metadata.sizeBytes != preliminary.declaredByteSize.Int64 || metadata.sizeBytes > mediaLimit(preliminary.mediaType) {
		return FinalizeUploadResult{}, ErrUploadInvalid
	}

	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return FinalizeUploadResult{}, errors.New("could not begin upload finalization")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return FinalizeUploadResult{}, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return FinalizeUploadResult{}, err
	}
	current, err := findUploadForUpdate(ctx, tx, workspaceID.String(), parsedAssetID)
	if errors.Is(err, sql.ErrNoRows) {
		return FinalizeUploadResult{}, ErrAssetNotFound
	}
	if err != nil {
		return FinalizeUploadResult{}, err
	}
	if current.sourceType != "UPLOAD" || current.status != "PENDING" {
		return FinalizeUploadResult{}, ErrUploadConflict
	}
	finalized, err = hasDurableAssetJob(ctx, tx, "PROCESS_UPLOAD", current.id)
	if err != nil {
		return FinalizeUploadResult{}, err
	}
	if finalized {
		if err := tx.Commit(); err != nil {
			return FinalizeUploadResult{}, errors.New("could not finish repeated upload finalization")
		}
		return FinalizeUploadResult{AssetID: current.id, Status: "PENDING", WorkflowEstablished: true}, nil
	}
	if s.clock().Sub(current.createdAt) >= uploadSessionLifetime {
		return FinalizeUploadResult{}, ErrUploadConflict
	}
	now := s.clock().UTC()
	if err := createDurableAssetJob(ctx, tx, "PROCESS_UPLOAD", current.id, now, now); err != nil {
		return FinalizeUploadResult{}, err
	}
	assetIDResult := current.id
	if err := tx.Commit(); err != nil {
		return FinalizeUploadResult{}, errors.New("could not commit upload processing workflow")
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "assets.upload.finalized", "module", "assets", "operation", "finalizeUpload", "entityId", assetIDResult, "workspaceId", workspaceID.String())
	}
	return FinalizeUploadResult{AssetID: assetIDResult, Status: "PENDING", WorkflowEstablished: true}, nil
}

func (s *Service) lockUploadMutation(ctx context.Context, subject workspace.MembershipSubject, workspaceID workspace.WorkspaceID, assetID string) (uploadState, bool, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return uploadState{}, false, errors.New("could not begin upload state check")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return uploadState{}, false, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return uploadState{}, false, err
	}
	asset, err := findUploadForUpdate(ctx, tx, workspaceID.String(), assetID)
	if errors.Is(err, sql.ErrNoRows) {
		return uploadState{}, false, ErrAssetNotFound
	}
	if err != nil {
		return uploadState{}, false, err
	}
	if asset.sourceType != "UPLOAD" || asset.status != "PENDING" {
		return uploadState{}, false, ErrUploadConflict
	}
	finalized, err := hasDurableAssetJob(ctx, tx, "PROCESS_UPLOAD", asset.id)
	if err != nil {
		return uploadState{}, false, err
	}
	if finalized {
		return asset, true, nil
	}
	if s.clock().Sub(asset.createdAt) >= uploadSessionLifetime {
		return uploadState{}, false, ErrUploadConflict
	}
	if err := tx.Commit(); err != nil {
		return uploadState{}, false, errors.New("could not finish upload state check")
	}
	return asset, false, nil
}

func findUploadForUpdate(ctx context.Context, tx *sql.Tx, workspaceID, assetID string) (uploadState, error) {
	var state uploadState
	err := tx.QueryRowContext(ctx, `
		SELECT id::text, workspace_id::text, status, source_type, media_type,
			created_at, staging_key, declared_byte_size
		FROM assets
		WHERE workspace_id = $1::uuid AND id = $2::uuid
		FOR UPDATE
	`, workspaceID, assetID).Scan(&state.id, &state.workspaceID, &state.status, &state.sourceType, &state.mediaType, &state.createdAt, &state.stagingKey, &state.declaredByteSize)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return uploadState{}, errors.New("could not read upload state")
	}
	return state, err
}

func hasDurableAssetJob(ctx context.Context, tx *sql.Tx, kind, assetID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM asset_jobs WHERE dedupe_key = $1)`, fmt.Sprintf("%s:%s", kind, assetID)).Scan(&exists)
	if err != nil {
		return false, errors.New("could not read Asset workflow")
	}
	return exists, nil
}

func parseUploadMutationIDs(workspaceIDText, assetIDText string) (workspace.WorkspaceID, string, error) {
	workspaceID, err := workspace.ParseID(workspaceIDText)
	if err != nil {
		return workspace.WorkspaceID{}, "", ErrUploadInvalid
	}
	assetID, err := uuid.Parse(assetIDText)
	if err != nil || assetID == uuid.Nil {
		return workspace.WorkspaceID{}, "", ErrUploadInvalid
	}
	return workspaceID, assetID.String(), nil
}

func authenticatedUserID(subject workspace.MembershipSubject) (string, bool) {
	if subject == nil {
		return "", false
	}
	return subject.AuthenticatedUserID()
}

func uploadCapabilityEligible(state uploadState, finalized bool, now time.Time) bool {
	return state.sourceType == "UPLOAD" && state.status == "PENDING" && !finalized && now.Sub(state.createdAt) < uploadSessionLifetime
}

func mediaLimit(value string) int64 {
	media, err := parseMediaType(value)
	if err != nil {
		return 0
	}
	return media.maxUploadBytes()
}

func (s *Service) issueUploadCapability(key stagingKey, contentLength int64) (UploadCapability, error) {
	expiresAt := s.clock().UTC().Add(uploadCapabilityLifetime)
	capabilityURL, err := s.storage.createStagingPutCapability(key, expiresAt, contentLength)
	if err != nil {
		return UploadCapability{}, ErrStorageUnavailable
	}
	return UploadCapability{URL: capabilityURL, ExpiresAt: expiresAt, ContentLength: contentLength}, nil
}
