package assets

import (
	"context"
	"database/sql"
	"errors"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

type deletionCleanupAsset struct {
	id            string
	workspaceID   string
	status        string
	stagingKey    sql.NullString
	permanentKey  sql.NullString
	deletingReady int
}

// CleanupDeletedAsset removes private objects before deleting their lifecycle
// row. The operation is safe to retry after interruption at either boundary.
func (s *Service) CleanupDeletedAsset(ctx context.Context, assetIDText string) error {
	finish, err := s.prepareDeletionCleanup(ctx, assetIDText, nil)
	if err != nil || finish == nil {
		return err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("could not begin Asset metadata cleanup")
	}
	defer tx.Rollback()
	if err := finish(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "assets.deletion_cleaned", "module", "assets", "operation", "deleteCleanup", "entityId", assetIDText)
	}
	return nil
}

// Return the final metadata mutation so the worker can commit it under its
// job lease in the same transaction as job completion.
func (s *Service) prepareDeletionCleanup(ctx context.Context, assetIDText string, beforeDelete func(context.Context) error) (assetJobMutation, error) {
	assetID, err := uuid.Parse(assetIDText)
	if err != nil || assetID == uuid.Nil {
		return nil, ErrAssetLibraryInvalid
	}
	asset, err := s.loadDeletionCleanupAsset(ctx, assetID.String())
	if errors.Is(err, sql.ErrNoRows) || err == nil && asset.status != "DELETING" {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not read Asset deletion state")
	}
	continueCleanup, err := s.ensureDeletionUnreferenced(ctx, asset)
	if err != nil {
		return nil, err
	}
	if !continueCleanup {
		return nil, nil
	}
	if asset.stagingKey.Valid {
		key, err := parseStagingKey(asset.stagingKey.String)
		if err != nil {
			return nil, errors.New("Asset deletion has invalid staging key")
		}
		if beforeDelete != nil {
			if err := beforeDelete(ctx); err != nil {
				return nil, err
			}
		}
		if err := deleteMissingStorageObject(func() error { return s.storage.deleteStagingObject(ctx, key) }); err != nil {
			return nil, ErrStorageUnavailable
		}
	}
	if asset.permanentKey.Valid {
		key, err := parsePermanentKey(asset.permanentKey.String)
		if err != nil {
			return nil, errors.New("Asset deletion has invalid permanent key")
		}
		_, found, err := s.storage.getObjectMetadata(ctx, key.String())
		if err != nil {
			return nil, ErrStorageUnavailable
		}
		if !found && asset.deletingReady == 1 && s.logger != nil {
			s.logger.WarnContext(ctx, "assets.ready_object_missing", "module", "assets", "operation", "deleteCleanup", "entityId", asset.id)
		}
		if beforeDelete != nil {
			if err := beforeDelete(ctx); err != nil {
				return nil, err
			}
		}
		if err := deleteMissingStorageObject(func() error { return s.storage.deletePermanentObject(ctx, key) }); err != nil {
			return nil, ErrStorageUnavailable
		}
	}
	return func(ctx context.Context, tx *sql.Tx) error {
		return deleteAssetMetadataAfterCleanup(ctx, tx, asset)
	}, nil
}

func (s *Service) loadDeletionCleanupAsset(ctx context.Context, assetID string) (deletionCleanupAsset, error) {
	var asset deletionCleanupAsset
	err := s.database.QueryRowContext(ctx, `
		SELECT id::text, workspace_id::text, status, staging_key, permanent_key, deleting_from_ready
		FROM assets WHERE id = $1::uuid
	`, assetID).Scan(&asset.id, &asset.workspaceID, &asset.status, &asset.stagingKey, &asset.permanentKey, &asset.deletingReady)
	return asset, err
}

func (s *Service) ensureDeletionUnreferenced(ctx context.Context, asset deletionCleanupAsset) (bool, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("could not begin Asset cleanup check")
	}
	defer tx.Rollback()
	workspaceID, err := workspace.ParseID(asset.workspaceID)
	if err != nil {
		return false, errors.New("Asset deletion has invalid Workspace")
	}
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return false, err
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM assets WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`, asset.id, asset.workspaceID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || err == nil && status != "DELETING" {
		return false, nil
	}
	if err != nil {
		return false, errors.New("could not lock Asset cleanup state")
	}
	referenced, err := assetHasReferences(ctx, tx, asset.workspaceID, asset.id)
	if err != nil {
		return false, err
	}
	if referenced {
		return false, ErrAssetInUse
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func deleteAssetMetadataAfterCleanup(ctx context.Context, tx *sql.Tx, asset deletionCleanupAsset) error {
	workspaceID, err := workspace.ParseID(asset.workspaceID)
	if err != nil {
		return errors.New("Asset deletion has invalid Workspace")
	}
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM assets WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`, asset.id, asset.workspaceID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || err == nil && status != "DELETING" {
		return nil
	}
	if err != nil {
		return errors.New("could not lock Asset metadata cleanup state")
	}
	referenced, err := assetHasReferences(ctx, tx, asset.workspaceID, asset.id)
	if err != nil {
		return err
	}
	if referenced {
		return ErrAssetInUse
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE id = $1::uuid AND workspace_id = $2::uuid AND status = 'DELETING'`, asset.id, asset.workspaceID); err != nil {
		return errors.New("could not delete Asset metadata")
	}
	return nil
}

func deleteMissingStorageObject(deleteObject func() error) error {
	err := deleteObject()
	if errors.Is(err, errStorageNotFound) {
		return nil
	}
	return err
}
