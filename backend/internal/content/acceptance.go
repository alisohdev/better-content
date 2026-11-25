package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/workspace"
)

type ContentVersion struct {
	ID                string          `json:"id"`
	ContentID         string          `json:"-"`
	VersionNumber     int             `json:"versionNumber"`
	Document          json.RawMessage `json:"document"`
	Source            string          `json:"source"`
	CreatedAt         time.Time       `json:"createdAt"`
	CreatedByName     *string         `json:"createdByName"`
	IsCurrentAccepted bool            `json:"isCurrentAccepted"`
}

type AcceptanceResult struct {
	Draft           DraftView      `json:"draft"`
	AcceptedVersion ContentVersion `json:"acceptedVersion"`
}

func (s *Service) Accept(ctx context.Context, subject workspace.MembershipSubject, target ContentTarget, expectedRevision int) (AcceptanceResult, error) {
	workspaceID, err := workspace.ParseID(target.WorkspaceID)
	if err != nil {
		return AcceptanceResult{}, ErrDraftValidation
	}
	contentID, err := parseContentUUID(target.ContentID)
	if err != nil || expectedRevision < 1 {
		return AcceptanceResult{}, ErrDraftValidation
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return AcceptanceResult{}, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return AcceptanceResult{}, errors.New("could not begin Content acceptance")
	}
	defer tx.Rollback()
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return AcceptanceResult{}, err
	}
	locked, err := lockContentWriteTarget(ctx, tx, workspaceID.String(), contentID)
	if err != nil {
		return AcceptanceResult{}, err
	}
	if locked.revision != expectedRevision {
		return AcceptanceResult{}, ErrDraftConflict
	}
	stored, err := document.ParseStored(locked.document)
	if err != nil {
		return AcceptanceResult{}, errors.New("stored Content Draft invariant is invalid")
	}
	legacyMigration := stored.SchemaVersion() == 1
	userID, _ := subject.AuthenticatedUserID()
	var accepted *ContentVersion
	var acceptedDocument document.Document
	if locked.acceptedVersionID.Valid {
		version, versionDocument, err := loadAcceptedContentVersion(ctx, tx, contentID, locked.acceptedVersionID.String)
		if err != nil {
			return AcceptanceResult{}, err
		}
		accepted = &version
		acceptedDocument = versionDocument
	}
	if legacyMigration {
		hasCheckpoint, err := hasLegacyDraftCheckpoint(ctx, tx, contentID)
		if err != nil {
			return AcceptanceResult{}, err
		}
		if hasCheckpoint {
			return AcceptanceResult{}, errors.New("legacy Content Draft checkpoint invariant is invalid")
		}
	}
	projected, err := document.ProjectToV4(stored)
	if err != nil {
		return AcceptanceResult{}, ErrDraftValidation
	}
	acceptedDraft := projected.Document()
	if err := validateReferencedAssets(ctx, tx, workspaceID.String(), acceptedDraft); err != nil {
		return AcceptanceResult{}, err
	}
	now := s.clock.Now().UTC()
	revision := locked.revision
	if legacyMigration {
		checkpointNumber, err := nextContentVersionNumber(ctx, tx, contentID)
		if err != nil {
			return AcceptanceResult{}, err
		}
		if _, err := insertContentVersion(ctx, tx, contentID, checkpointNumber, stored.JSON(), "LEGACY_DRAFT_CHECKPOINT", nil, userID, now); err != nil {
			return AcceptanceResult{}, err
		}
		revision++
		if err := updateDraftDocument(ctx, tx, contentID, locked.revision, revision, acceptedDraft.JSON(), now); err != nil {
			return AcceptanceResult{}, err
		}
	}
	if !legacyMigration && accepted != nil && document.Equal(acceptedDocument, acceptedDraft) {
		draftView, err := makeDraftView(stored, revision, locked.updatedAt)
		if err != nil {
			return AcceptanceResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcceptanceResult{}, errors.New("could not finish Content acceptance no-op")
		}
		accepted.Document = acceptedDraft.JSON()
		accepted.IsCurrentAccepted = true
		return AcceptanceResult{Draft: draftView, AcceptedVersion: *accepted}, nil
	}
	versionNumber, err := nextContentVersionNumber(ctx, tx, contentID)
	if err != nil {
		return AcceptanceResult{}, err
	}
	versionID, err := insertContentVersion(ctx, tx, contentID, versionNumber, acceptedDraft.JSON(), "CREATOR_ACCEPTED", nil, userID, now)
	if err != nil {
		return AcceptanceResult{}, err
	}
	if err := reconcileAssetReferences(ctx, tx, workspaceID.String(), contentID, "VERSION", &versionID, acceptedDraft.AssetReferences()); err != nil {
		return AcceptanceResult{}, err
	}
	draftUpdatedAt := locked.updatedAt
	if legacyMigration {
		draftUpdatedAt = now
	}
	draftView, err := makeDraftView(acceptedDraft, revision, draftUpdatedAt)
	if err != nil {
		return AcceptanceResult{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE contents SET accepted_version_id = $3::uuid WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID.String(), contentID, versionID)
	if err != nil {
		return AcceptanceResult{}, errors.New("could not set accepted Content Version")
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return AcceptanceResult{}, ErrContentNotFound
	}
	if err := tx.Commit(); err != nil {
		return AcceptanceResult{}, errors.New("could not commit Content acceptance")
	}
	return AcceptanceResult{Draft: draftView, AcceptedVersion: ContentVersion{
		ID: versionID, ContentID: contentID, VersionNumber: versionNumber, Document: acceptedDraft.JSON(), Source: "CREATOR_ACCEPTED", CreatedAt: now, IsCurrentAccepted: true,
	}}, nil
}

func loadAcceptedContentVersion(ctx context.Context, tx *sql.Tx, contentID, versionID string) (ContentVersion, document.Document, error) {
	var version ContentVersion
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT id::text, content_id::text, version_number, document, source, created_at FROM content_versions WHERE content_id = $1::uuid AND id = $2::uuid FOR UPDATE`, contentID, versionID).Scan(&version.ID, &version.ContentID, &version.VersionNumber, &raw, &version.Source, &version.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentVersion{}, document.Document{}, errors.New("accepted Content Version is missing")
	}
	if err != nil {
		return ContentVersion{}, document.Document{}, errors.New("could not load accepted Content Version")
	}
	version.IsCurrentAccepted = true
	if version.Source != "CREATOR_ACCEPTED" {
		return ContentVersion{}, document.Document{}, errors.New("accepted Content Version source is invalid")
	}
	parsed, err := document.ParseStored(raw)
	if err != nil {
		return ContentVersion{}, document.Document{}, errors.New("accepted Content Version document is invalid")
	}
	version.Document = parsed.JSON()
	return version, parsed, nil
}

func updateDraftDocument(ctx context.Context, tx *sql.Tx, contentID string, oldRevision, newRevision int, raw []byte, updatedAt time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE content_drafts SET document = $2::jsonb, revision = $3, updated_at = $4 WHERE content_id = $1::uuid AND revision = $5`, contentID, raw, newRevision, updatedAt, oldRevision)
	if err != nil {
		return errors.New("could not migrate legacy Content Draft")
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return ErrDraftConflict
	}
	return nil
}
