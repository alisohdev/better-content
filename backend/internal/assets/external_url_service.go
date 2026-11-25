package assets

import (
	"context"
	"errors"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

type ExternalURLInput struct {
	WorkspaceID string
	MediaType   string
	DisplayName string
	SourceURL   string
}

type ExternalURLResult struct {
	AssetID    string `json:"assetId"`
	Status     string `json:"status"`
	SourceType string `json:"sourceType"`
	MediaType  string `json:"mediaType"`
	SourceHost string `json:"sourceHost"`
}

// CreateExternalURLAsset stores only a validated HTTPS origin and queues the
// bounded remote acquisition workflow; it does not fetch during the request.
func (s *Service) CreateExternalURLAsset(ctx context.Context, subject workspace.MembershipSubject, input ExternalURLInput) (ExternalURLResult, error) {
	userID, authenticated := authenticatedUserID(subject)
	if !authenticated || userID == "" {
		return ExternalURLResult{}, ErrUnauthenticated
	}
	workspaceID, err := workspace.ParseID(input.WorkspaceID)
	if err != nil {
		return ExternalURLResult{}, ErrExternalURLInvalid
	}
	media, err := parseMediaType(input.MediaType)
	if err != nil {
		return ExternalURLResult{}, ErrExternalURLInvalid
	}
	displayName, err := normalizeSafeName(input.DisplayName, 200)
	if err != nil {
		return ExternalURLResult{}, ErrExternalURLInvalid
	}
	remote, err := normalizeExternalMediaURL(input.SourceURL)
	if err != nil {
		return ExternalURLResult{}, ErrExternalURLInvalid
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return ExternalURLResult{}, err
	}
	stagingKey, err := newStagingKey()
	if err != nil {
		return ExternalURLResult{}, errors.New("could not allocate Asset staging key")
	}
	now := s.clock().UTC()
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ExternalURLResult{}, errors.New("could not begin external Asset admission")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return ExternalURLResult{}, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return ExternalURLResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID); err != nil {
		return ExternalURLResult{}, errors.New("could not serialize Asset admission")
	}
	since := now.Add(-time.Hour)
	var userEvents, workspaceEvents, activeWorkflows int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM asset_admission_events WHERE user_id = $1 AND created_at >= $2`, userID, since).Scan(&userEvents); err != nil {
		return ExternalURLResult{}, errors.New("could not read user Asset admission quota")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM asset_admission_events WHERE workspace_id = $1::uuid AND created_at >= $2`, workspaceID.String(), since).Scan(&workspaceEvents); err != nil {
		return ExternalURLResult{}, errors.New("could not read Workspace Asset admission quota")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE workspace_id = $1::uuid AND status IN ('PENDING', 'PROCESSING')`, workspaceID.String()).Scan(&activeWorkflows); err != nil {
		return ExternalURLResult{}, errors.New("could not read active Asset workflows")
	}
	if userEvents >= maxUserAssetAdmissions || workspaceEvents >= maxWorkspaceAdmissions || activeWorkflows >= maxActiveAssetWorkflows {
		return ExternalURLResult{}, ErrUploadLimited
	}
	assetID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO assets (
			id, workspace_id, created_by_user_id, media_type, source_type, status,
			display_name, source_url, source_host, staging_key, created_at, updated_at
		) VALUES ($1::uuid, $2::uuid, $3, $4, 'EXTERNAL_URL', 'PENDING', $5, $6, $7, $8, $9, $9)
	`, assetID, workspaceID.String(), userID, media.String(), string(displayName), remote.sourceURL, remote.sourceHost, stagingKey.String(), now); err != nil {
		return ExternalURLResult{}, errors.New("could not create external Asset")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO asset_admission_events (workspace_id, user_id, created_at) VALUES ($1::uuid, $2, $3)`, workspaceID.String(), userID, now); err != nil {
		return ExternalURLResult{}, errors.New("could not record Asset admission")
	}
	if err := createDurableAssetJob(ctx, tx, "INGEST_EXTERNAL_URL", assetID, now, now); err != nil {
		return ExternalURLResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExternalURLResult{}, errors.New("could not commit external Asset admission")
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "assets.external_url.created", "module", "assets", "operation", "createExternalUrlAsset", "entityId", assetID, "userId", userID, "workspaceId", workspaceID.String())
	}
	return ExternalURLResult{AssetID: assetID, Status: "PENDING", SourceType: "EXTERNAL_URL", MediaType: media.String(), SourceHost: remote.sourceHost}, nil
}
