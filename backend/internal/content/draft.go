package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	assetdomain "bettercontent/backend/internal/assets"
	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

var (
	ErrDraftValidation = errors.New("Content Draft submission is invalid")
	ErrDraftConflict   = errors.New("Content Draft revision changed")
	ErrContentNotFound = errors.New("Content was not found")
)

type ContentTarget struct {
	WorkspaceID string
	ContentID   string
}

type SaveDraftInput struct {
	BaseRevision int
	Document     []byte
}

type DraftView struct {
	Revision       int             `json:"revision"`
	Document       json.RawMessage `json:"document"`
	EditorDocument json.RawMessage `json:"editorDocument"`
	V2Projection   json.RawMessage `json:"v2Projection"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type contentWriteTarget struct {
	contentID         string
	acceptedVersionID sql.NullString
	document          []byte
	revision          int
	updatedAt         time.Time
}

func (s *Service) SaveDraft(ctx context.Context, subject workspace.MembershipSubject, target ContentTarget, input SaveDraftInput) (DraftView, error) {
	workspaceID, err := workspace.ParseID(target.WorkspaceID)
	if err != nil {
		return DraftView{}, ErrDraftValidation
	}
	contentID, err := parseContentUUID(target.ContentID)
	if err != nil || input.BaseRevision < 1 {
		return DraftView{}, ErrDraftValidation
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return DraftView{}, err
	}
	submitted, err := document.ParseSubmitted(input.Document)
	if err != nil {
		return DraftView{}, ErrDraftValidation
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return DraftView{}, errors.New("could not begin Content Draft save")
	}
	defer tx.Rollback()
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return DraftView{}, err
	}
	locked, err := lockContentWriteTarget(ctx, tx, workspaceID.String(), contentID)
	if err != nil {
		return DraftView{}, err
	}
	if locked.revision != input.BaseRevision {
		return DraftView{}, ErrDraftConflict
	}
	stored, err := document.ParseStored(locked.document)
	if err != nil {
		return DraftView{}, errors.New("stored Content Draft invariant is invalid")
	}
	if stored.SchemaVersion() == 1 && document.Equal(stored, submitted) {
		view, err := makeDraftView(stored, locked.revision, locked.updatedAt)
		if err != nil {
			return DraftView{}, err
		}
		if err := tx.Commit(); err != nil {
			return DraftView{}, errors.New("could not finish Content Draft no-op")
		}
		return view, nil
	}
	if stored.SchemaVersion() == 3 || stored.SchemaVersion() == 4 {
		if submitted.SchemaVersion() != 4 {
			return DraftView{}, ErrDraftValidation
		}
	}
	if stored.SchemaVersion() == 2 && submitted.SchemaVersion() == 1 {
		return DraftView{}, ErrDraftValidation
	}
	projected, err := document.ProjectToV4(submitted)
	if err != nil {
		return DraftView{}, ErrDraftValidation
	}
	persisted := projected.Document()
	if err := validateReferencedAssets(ctx, tx, workspaceID.String(), persisted); err != nil {
		return DraftView{}, err
	}
	if stored.SchemaVersion() == 1 {
		hasCheckpoint, err := hasLegacyDraftCheckpoint(ctx, tx, contentID)
		if err != nil {
			return DraftView{}, err
		}
		if hasCheckpoint {
			return DraftView{}, errors.New("legacy Content Draft checkpoint invariant is invalid")
		}
		versionNumber, err := nextContentVersionNumber(ctx, tx, contentID)
		if err != nil {
			return DraftView{}, err
		}
		userID, _ := subject.AuthenticatedUserID()
		if _, err := insertContentVersion(ctx, tx, contentID, versionNumber, stored.JSON(), "LEGACY_DRAFT_CHECKPOINT", nil, userID, s.clock.Now().UTC()); err != nil {
			return DraftView{}, err
		}
	}
	now := s.clock.Now().UTC()
	canonical := persisted.JSON()
	updated, err := tx.ExecContext(ctx, `UPDATE content_drafts SET document = $2::jsonb, revision = revision + 1, updated_at = $3 WHERE content_id = $1::uuid AND revision = $4`, contentID, canonical, now, locked.revision)
	if err != nil {
		return DraftView{}, errors.New("could not save Content Draft")
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return DraftView{}, ErrDraftConflict
	}
	if err := reconcileAssetReferences(ctx, tx, workspaceID.String(), contentID, "DRAFT", nil, persisted.AssetReferences()); err != nil {
		return DraftView{}, err
	}
	view, err := makeDraftView(persisted, locked.revision+1, now)
	if err != nil {
		return DraftView{}, err
	}
	if err := tx.Commit(); err != nil {
		return DraftView{}, errors.New("could not commit Content Draft save")
	}
	return view, nil
}

func makeDraftView(stored document.Document, revision int, updatedAt time.Time) (DraftView, error) {
	editor, err := document.ProjectToV4(stored)
	if err != nil {
		return DraftView{}, ErrDraftValidation
	}
	v2, err := document.ProjectToV2(stored)
	if err != nil {
		return DraftView{}, ErrDraftValidation
	}
	return DraftView{
		Revision:       revision,
		Document:       stored.JSON(),
		EditorDocument: editor.Document().JSON(),
		V2Projection:   v2.JSON(),
		UpdatedAt:      updatedAt,
	}, nil
}

func lockContentWriteTarget(ctx context.Context, tx *sql.Tx, workspaceID, contentID string) (contentWriteTarget, error) {
	var target contentWriteTarget
	err := tx.QueryRowContext(ctx, `SELECT c.id::text, c.accepted_version_id::text, d.document, d.revision, d.updated_at FROM contents c JOIN content_drafts d ON d.content_id = c.id WHERE c.workspace_id = $1::uuid AND c.id = $2::uuid FOR UPDATE OF c, d`, workspaceID, contentID).Scan(&target.contentID, &target.acceptedVersionID, &target.document, &target.revision, &target.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return contentWriteTarget{}, ErrContentNotFound
	}
	if err != nil {
		return contentWriteTarget{}, errors.New("could not lock Content Draft")
	}
	return target, nil
}

func hasLegacyDraftCheckpoint(ctx context.Context, queryer generationQueryer, contentID string) (bool, error) {
	var exists bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM content_versions WHERE content_id = $1::uuid AND source = 'LEGACY_DRAFT_CHECKPOINT')`, contentID).Scan(&exists)
	if err != nil {
		return false, errors.New("could not check legacy Content Draft checkpoint")
	}
	return exists, nil
}

func nextContentVersionNumber(ctx context.Context, queryer generationQueryer, contentID string) (int, error) {
	var next int
	err := queryer.QueryRowContext(ctx, `SELECT COALESCE(max(version_number), 0) + 1 FROM content_versions WHERE content_id = $1::uuid`, contentID).Scan(&next)
	if err != nil || next < 1 {
		return 0, errors.New("could not determine next Content Version number")
	}
	return next, nil
}

func insertContentVersion(ctx context.Context, tx *sql.Tx, contentID string, number int, raw []byte, source string, aiRunID *string, userID string, createdAt time.Time) (string, error) {
	versionID := uuid.NewString()
	var runID any
	if aiRunID != nil {
		runID = *aiRunID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO content_versions (id, content_id, version_number, document, source, ai_run_id, created_by_user_id, created_at) VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6::uuid, $7, $8)`, versionID, contentID, number, raw, source, runID, userID, createdAt); err != nil {
		return "", errors.New("could not create immutable Content Version")
	}
	return versionID, nil
}

func validateReferencedAssets(ctx context.Context, tx *sql.Tx, workspaceID string, doc document.Document) error {
	if err := assetdomain.ValidateAttachments(ctx, tx, workspaceID, doc); errors.Is(err, assetdomain.ErrAttachmentInvalid) {
		return ErrDraftValidation
	} else {
		return err
	}
}

func reconcileAssetReferences(ctx context.Context, tx *sql.Tx, workspaceID, contentID, artifactKind string, versionID *string, references []document.AssetReference) error {
	if artifactKind == "DRAFT" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM asset_references WHERE workspace_id = $1::uuid AND content_id = $2::uuid AND artifact_kind = 'DRAFT' AND version_id IS NULL`, workspaceID, contentID); err != nil {
			return errors.New("could not replace Content Draft Asset references")
		}
	} else if artifactKind == "VERSION" && versionID != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM asset_references WHERE workspace_id = $1::uuid AND content_id = $2::uuid AND artifact_kind = 'VERSION' AND version_id = $3::uuid`, workspaceID, contentID, *versionID); err != nil {
			return errors.New("could not replace Content Version Asset references")
		}
	} else {
		return errors.New("Content Asset reference artifact is invalid")
	}
	for _, reference := range references {
		var version any
		if versionID != nil {
			version = *versionID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO asset_references (workspace_id, asset_id, content_id, artifact_kind, version_id, direction_id) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid)`, workspaceID, reference.AssetID, contentID, artifactKind, version, reference.DirectionID); err != nil {
			return errors.New("could not save Content Asset references")
		}
	}
	return nil
}
