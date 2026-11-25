package assets

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

type DeleteAssetInput struct {
	WorkspaceID string
	AssetID     string
}

type DeleteAssetResult struct {
	AssetID                    string `json:"assetId"`
	Status                     string `json:"status"`
	CleanupWorkflowEstablished bool   `json:"cleanupWorkflowEstablished"`
}

var ErrAssetInUse = errors.New("Asset is still referenced by Content")

// DeleteAsset requests reference-safe deletion. Storage cleanup is deferred to
// the durable worker workflow after the database transaction commits.
func (s *Service) DeleteAsset(ctx context.Context, subject workspace.MembershipSubject, input DeleteAssetInput) (DeleteAssetResult, error) {
	userID, authenticated := authenticatedUserID(subject)
	if !authenticated || userID == "" {
		return DeleteAssetResult{}, ErrUnauthenticated
	}
	workspaceID, err := workspace.ParseID(input.WorkspaceID)
	if err != nil {
		return DeleteAssetResult{}, ErrAssetLibraryInvalid
	}
	assetID, err := uuid.Parse(input.AssetID)
	if err != nil || assetID == uuid.Nil {
		return DeleteAssetResult{}, ErrAssetLibraryInvalid
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return DeleteAssetResult{}, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return DeleteAssetResult{}, errors.New("could not begin Asset deletion")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return DeleteAssetResult{}, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return DeleteAssetResult{}, err
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM assets WHERE workspace_id = $1::uuid AND id = $2::uuid FOR UPDATE`, workspaceID.String(), assetID.String()).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteAssetResult{}, ErrAssetNotFound
	}
	if err != nil {
		return DeleteAssetResult{}, errors.New("could not read Asset deletion state")
	}
	if status != "DELETING" {
		deletingFromReady, allowed := deletionOrigin(status)
		if !allowed {
			return DeleteAssetResult{}, ErrAssetConflict
		}
		referenced, err := assetHasReferences(ctx, tx, workspaceID.String(), assetID.String())
		if err != nil {
			return DeleteAssetResult{}, err
		}
		if referenced {
			return DeleteAssetResult{}, ErrAssetInUse
		}
		now := s.clock().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = 'DELETING', deleting_from_ready = $3, updated_at = $4 WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID.String(), assetID.String(), deletingFromReady, now); err != nil {
			return DeleteAssetResult{}, errors.New("could not mark Asset for deletion")
		}
	}
	now := s.clock().UTC()
	if err := createDurableAssetJob(ctx, tx, "DELETE_ASSET", assetID.String(), now, now); err != nil {
		return DeleteAssetResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeleteAssetResult{}, errors.New("could not commit Asset deletion")
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "assets.deletion_requested", "module", "assets", "operation", "deleteAsset", "workspaceId", workspaceID.String(), "userId", userID, "entityId", assetID.String())
	}
	return DeleteAssetResult{AssetID: assetID.String(), Status: "DELETING", CleanupWorkflowEstablished: true}, nil
}

func deletionOrigin(status string) (int, bool) {
	switch status {
	case "PENDING", "FAILED":
		return 0, true
	case "READY":
		return 1, true
	default:
		return 0, false
	}
}

func assetHasReferences(ctx context.Context, tx *sql.Tx, workspaceID, assetID string) (bool, error) {
	var projected bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM asset_references WHERE workspace_id = $1::uuid AND asset_id = $2::uuid)`, workspaceID, assetID).Scan(&projected); err != nil {
		return false, errors.New("could not check Asset references")
	}
	if projected {
		return true, nil
	}
	for _, query := range []string{
		`SELECT d.document FROM content_drafts AS d INNER JOIN contents AS c ON c.id = d.content_id WHERE c.workspace_id = $1::uuid`,
		`SELECT v.document FROM content_versions AS v INNER JOIN contents AS c ON c.id = v.content_id WHERE c.workspace_id = $1::uuid`,
	} {
		rows, err := tx.QueryContext(ctx, query, workspaceID)
		if err != nil {
			return false, errors.New("could not check canonical Content references")
		}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return false, errors.New("could not read canonical Content references")
			}
			parsed, err := document.ParseStored(raw)
			if err != nil {
				rows.Close()
				return false, errors.New("canonical Content reference data is invalid")
			}
			for _, reference := range parsed.AssetReferences() {
				if strings.EqualFold(reference.AssetID, assetID) {
					rows.Close()
					return true, nil
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, errors.New("could not read canonical Content references")
		}
		if err := rows.Close(); err != nil {
			return false, errors.New("could not close canonical Content reference query")
		}
	}
	return false, nil
}
