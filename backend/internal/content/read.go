package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/workspace"
)

type ContentListItem struct {
	ID              string    `json:"id"`
	SourceIdeaTitle string    `json:"sourceIdeaTitle"`
	Format          string    `json:"format"`
	ContentLanguage string    `json:"contentLanguage"`
	LastEditedAt    time.Time `json:"lastEditedAt"`
}

type ContentSourceIdea struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type ContentVersionView struct {
	ID                string          `json:"id"`
	VersionNumber     int             `json:"versionNumber"`
	Document          json.RawMessage `json:"document"`
	Source            string          `json:"source"`
	CreatedAt         time.Time       `json:"createdAt"`
	CreatedByName     *string         `json:"createdByName"`
	IsCurrentAccepted bool            `json:"isCurrentAccepted"`
}

type ContentAssetPresentation struct {
	DisplayName string `json:"displayName"`
	MediaType   string `json:"mediaType"`
}

type ContentDetailView struct {
	ID                 string                              `json:"id"`
	SourceIdea         ContentSourceIdea                   `json:"sourceIdea"`
	ContentLanguage    string                              `json:"contentLanguage"`
	Format             string                              `json:"format"`
	Draft              DraftView                           `json:"draft"`
	AcceptedVersionID  *string                             `json:"acceptedVersionId"`
	Versions           []ContentVersionView                `json:"versions"`
	AssetPresentations map[string]ContentAssetPresentation `json:"assetPresentations"`
}

type EditGuideAssetPresentation struct {
	DisplayName string `json:"displayName"`
	MediaType   string `json:"mediaType"`
	Width       *int   `json:"width"`
	Height      *int   `json:"height"`
	Previewable bool   `json:"previewable"`
}

type ProductionView struct {
	Status                string                                `json:"status"`
	ContentID             string                                `json:"contentId"`
	ContentTitle          string                                `json:"contentTitle"`
	ContentLanguage       string                                `json:"contentLanguage"`
	AcceptedVersionID     *string                               `json:"acceptedVersionId"`
	AcceptedVersionNumber *int                                  `json:"acceptedVersionNumber,omitempty"`
	ScriptBlocks          any                                   `json:"scriptBlocks,omitempty"`
	AssetPresentations    map[string]EditGuideAssetPresentation `json:"assetPresentations,omitempty"`
}

type contentReadQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type contentReadIdentity struct {
	id                string
	sourceIdeaID      string
	sourceIdeaTitle   string
	contentLanguage   string
	format            string
	acceptedVersionID sql.NullString
}

type contentDetailRecord struct {
	contentReadIdentity
	document  []byte
	revision  int
	updatedAt time.Time
}

func (s *Service) List(ctx context.Context, subject workspace.MembershipSubject, workspaceValue string) ([]ContentListItem, error) {
	workspaceID, err := workspace.ParseID(workspaceValue)
	if err != nil {
		return nil, ErrDraftValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.database.QueryContext(ctx, `
		SELECT c.id::text, i.title, c.format, c.content_language, d.updated_at
		FROM contents c
		JOIN content_drafts d ON d.content_id = c.id
		JOIN ideas i ON i.id = c.source_idea_id
		JOIN idea_generation_batches b ON b.id = i.batch_id AND b.workspace_id = c.workspace_id
		WHERE c.workspace_id = $1::uuid
		ORDER BY d.updated_at DESC, c.id DESC`, workspaceID.String())
	if err != nil {
		return nil, errors.New("could not list Content")
	}
	defer rows.Close()
	result := make([]ContentListItem, 0)
	for rows.Next() {
		var item ContentListItem
		if err := rows.Scan(&item.ID, &item.SourceIdeaTitle, &item.Format, &item.ContentLanguage, &item.LastEditedAt); err != nil {
			return nil, errors.New("could not read Content list")
		}
		if !validContentMetadata(item.ContentLanguage, item.Format) {
			return nil, errors.New("stored Content metadata invariant is invalid")
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read Content list")
	}
	return result, nil
}

func (s *Service) Detail(ctx context.Context, subject workspace.MembershipSubject, target ContentTarget) (ContentDetailView, error) {
	workspaceID, err := workspace.ParseID(target.WorkspaceID)
	if err != nil {
		return ContentDetailView{}, ErrDraftValidation
	}
	contentID, err := parseContentUUID(target.ContentID)
	if err != nil {
		return ContentDetailView{}, ErrDraftValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return ContentDetailView{}, err
	}
	record, err := loadContentDetail(ctx, s.database, workspaceID.String(), contentID)
	if err != nil {
		return ContentDetailView{}, err
	}
	if !validContentMetadata(record.contentLanguage, record.format) {
		return ContentDetailView{}, errors.New("stored Content metadata invariant is invalid")
	}
	stored, err := document.ParseStored(record.document)
	if err != nil || record.revision < 1 {
		return ContentDetailView{}, errors.New("stored Content Draft invariant is invalid")
	}
	draft, err := makeDraftView(stored, record.revision, record.updatedAt)
	if err != nil {
		return ContentDetailView{}, err
	}
	versions, err := loadContentVersions(ctx, s.database, workspaceID.String(), contentID, record.acceptedVersionID)
	if err != nil {
		return ContentDetailView{}, err
	}
	if record.acceptedVersionID.Valid && !hasAcceptedVersion(versions, record.acceptedVersionID.String) {
		return ContentDetailView{}, errors.New("accepted Content Version invariant is invalid")
	}
	assetPresentations, err := loadVersionAssetPresentations(ctx, s.database, workspaceID.String(), versions)
	if err != nil {
		return ContentDetailView{}, err
	}
	var acceptedID *string
	if record.acceptedVersionID.Valid {
		value := record.acceptedVersionID.String
		acceptedID = &value
	}
	return ContentDetailView{
		ID:                 contentID,
		SourceIdea:         ContentSourceIdea{ID: record.sourceIdeaID, Title: record.sourceIdeaTitle},
		ContentLanguage:    record.contentLanguage,
		Format:             record.format,
		Draft:              draft,
		AcceptedVersionID:  acceptedID,
		Versions:           versions,
		AssetPresentations: assetPresentations,
	}, nil
}

func (s *Service) Teleprompter(ctx context.Context, subject workspace.MembershipSubject, target ContentTarget) (ProductionView, error) {
	return s.productionView(ctx, subject, target, false)
}

func (s *Service) EditGuide(ctx context.Context, subject workspace.MembershipSubject, target ContentTarget) (ProductionView, error) {
	return s.productionView(ctx, subject, target, true)
}

func (s *Service) productionView(ctx context.Context, subject workspace.MembershipSubject, target ContentTarget, editGuide bool) (ProductionView, error) {
	workspaceID, err := workspace.ParseID(target.WorkspaceID)
	if err != nil {
		return ProductionView{}, ErrDraftValidation
	}
	contentID, err := parseContentUUID(target.ContentID)
	if err != nil {
		return ProductionView{}, ErrDraftValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return ProductionView{}, err
	}
	identity, err := loadContentReadIdentity(ctx, s.database, workspaceID.String(), contentID)
	if err != nil {
		return ProductionView{}, err
	}
	if !validContentMetadata(identity.contentLanguage, identity.format) {
		return ProductionView{}, errors.New("stored Content metadata invariant is invalid")
	}
	view := ProductionView{
		Status:          "NO_ACCEPTED_VERSION",
		ContentID:       identity.id,
		ContentTitle:    identity.sourceIdeaTitle,
		ContentLanguage: identity.contentLanguage,
	}
	if !identity.acceptedVersionID.Valid {
		return view, nil
	}
	acceptedID := identity.acceptedVersionID.String
	view.AcceptedVersionID = &acceptedID
	version, err := loadProductionVersion(ctx, s.database, workspaceID.String(), contentID, acceptedID)
	if errors.Is(err, sql.ErrNoRows) {
		return unavailableProductionView(view), nil
	}
	if err != nil {
		return ProductionView{}, errors.New("could not load accepted Content Version")
	}
	if version.Source != "CREATOR_ACCEPTED" || version.VersionNumber < 1 {
		return unavailableProductionView(view), nil
	}
	parsed, err := document.ParseStored(version.Document)
	if err != nil {
		return unavailableProductionView(view), nil
	}
	versionNumber := version.VersionNumber
	view.Status = "READY"
	view.AcceptedVersionNumber = &versionNumber
	if editGuide {
		blocks, err := document.EditGuide(parsed)
		if err != nil {
			return unavailableProductionView(view), nil
		}
		assets, err := loadEditGuideAssetPresentations(ctx, s.database, workspaceID.String(), parsed)
		if err != nil {
			return ProductionView{}, err
		}
		view.ScriptBlocks = blocks
		view.AssetPresentations = assets
		return view, nil
	}
	blocks, err := document.Teleprompter(parsed)
	if err != nil {
		return unavailableProductionView(view), nil
	}
	view.ScriptBlocks = blocks
	return view, nil
}

func loadContentDetail(ctx context.Context, queryer contentReadQueryer, workspaceID, contentID string) (contentDetailRecord, error) {
	var result contentDetailRecord
	err := queryer.QueryRowContext(ctx, `
		SELECT c.id::text, c.source_idea_id::text, i.title, c.content_language, c.format,
		       c.accepted_version_id::text, d.document, d.revision, d.updated_at
		FROM contents c
		JOIN content_drafts d ON d.content_id = c.id
		JOIN ideas i ON i.id = c.source_idea_id
		JOIN idea_generation_batches b ON b.id = i.batch_id AND b.workspace_id = c.workspace_id
		WHERE c.workspace_id = $1::uuid AND c.id = $2::uuid`, workspaceID, contentID).Scan(
		&result.id, &result.sourceIdeaID, &result.sourceIdeaTitle, &result.contentLanguage, &result.format,
		&result.acceptedVersionID, &result.document, &result.revision, &result.updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return contentDetailRecord{}, ErrContentNotFound
	}
	if err != nil {
		return contentDetailRecord{}, errors.New("could not load Content detail")
	}
	return result, nil
}

func loadContentReadIdentity(ctx context.Context, queryer contentReadQueryer, workspaceID, contentID string) (contentReadIdentity, error) {
	var result contentReadIdentity
	err := queryer.QueryRowContext(ctx, `
		SELECT c.id::text, c.source_idea_id::text, i.title, c.content_language, c.format, c.accepted_version_id::text
		FROM contents c
		JOIN ideas i ON i.id = c.source_idea_id
		JOIN idea_generation_batches b ON b.id = i.batch_id AND b.workspace_id = c.workspace_id
		WHERE c.workspace_id = $1::uuid AND c.id = $2::uuid`, workspaceID, contentID).Scan(
		&result.id, &result.sourceIdeaID, &result.sourceIdeaTitle, &result.contentLanguage, &result.format, &result.acceptedVersionID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return contentReadIdentity{}, ErrContentNotFound
	}
	if err != nil {
		return contentReadIdentity{}, errors.New("could not load Content")
	}
	return result, nil
}

func loadContentVersions(ctx context.Context, queryer contentReadQueryer, workspaceID, contentID string, acceptedID sql.NullString) ([]ContentVersionView, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT v.id::text, v.version_number, v.document, v.source, v.created_at, u.name
		FROM content_versions v
		JOIN contents c ON c.id = v.content_id AND c.workspace_id = $1::uuid
		JOIN "user" u ON u.id = v.created_by_user_id
		WHERE v.content_id = $2::uuid
		ORDER BY v.version_number DESC, v.created_at DESC`, workspaceID, contentID)
	if err != nil {
		return nil, errors.New("could not list Content Versions")
	}
	defer rows.Close()
	result := make([]ContentVersionView, 0)
	for rows.Next() {
		var version ContentVersionView
		var raw []byte
		var name sql.NullString
		if err := rows.Scan(&version.ID, &version.VersionNumber, &raw, &version.Source, &version.CreatedAt, &name); err != nil {
			return nil, errors.New("could not read Content Versions")
		}
		parsed, err := document.ParseStored(raw)
		if err != nil || version.VersionNumber < 1 || !validContentVersionSource(version.Source) {
			return nil, errors.New("stored Content Version invariant is invalid")
		}
		version.Document = parsed.JSON()
		if name.Valid {
			value := name.String
			version.CreatedByName = &value
		}
		version.IsCurrentAccepted = acceptedID.Valid && acceptedID.String == version.ID
		result = append(result, version)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read Content Versions")
	}
	return result, nil
}

func loadProductionVersion(ctx context.Context, queryer contentReadQueryer, workspaceID, contentID, versionID string) (ContentVersion, error) {
	var version ContentVersion
	err := queryer.QueryRowContext(ctx, `
		SELECT v.id::text, v.content_id::text, v.version_number, v.document, v.source, v.created_at
		FROM content_versions v
		JOIN contents c ON c.id = v.content_id AND c.workspace_id = $1::uuid
		WHERE v.content_id = $2::uuid AND v.id = $3::uuid`, workspaceID, contentID, versionID).Scan(
		&version.ID, &version.ContentID, &version.VersionNumber, &version.Document, &version.Source, &version.CreatedAt,
	)
	return version, err
}

func loadVersionAssetPresentations(ctx context.Context, queryer contentReadQueryer, workspaceID string, versions []ContentVersionView) (map[string]ContentAssetPresentation, error) {
	ids := make(map[string]struct{})
	for _, version := range versions {
		parsed, err := document.ParseStored(version.Document)
		if err != nil {
			return nil, errors.New("stored Content Version invariant is invalid")
		}
		for _, reference := range parsed.AssetReferences() {
			ids[reference.AssetID] = struct{}{}
		}
	}
	return queryAssetPresentations(ctx, queryer, workspaceID, ids)
}

func loadEditGuideAssetPresentations(ctx context.Context, queryer contentReadQueryer, workspaceID string, value document.Document) (map[string]EditGuideAssetPresentation, error) {
	allowed := make(map[string]map[string]struct{})
	for _, reference := range value.AssetReferences() {
		types := allowed[reference.AssetID]
		if types == nil {
			types = make(map[string]struct{})
			allowed[reference.AssetID] = types
		}
		if reference.DirectionType == "BROLL_CUE" {
			types["IMAGE"] = struct{}{}
			types["VIDEO"] = struct{}{}
		} else if reference.DirectionType == "SOUND_CUE" {
			types["AUDIO"] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return map[string]EditGuideAssetPresentation{}, nil
	}
	ids := make(map[string]struct{}, len(allowed))
	for id := range allowed {
		ids[id] = struct{}{}
	}
	base, err := queryAssetPresentations(ctx, queryer, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	placeholders, args := uuidPlaceholders(workspaceID, ids)
	rows, err := queryer.QueryContext(ctx, `SELECT id::text, status, width, height FROM assets WHERE workspace_id = $1::uuid AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, errors.New("could not load Edit Guide Asset metadata")
	}
	defer rows.Close()
	result := make(map[string]EditGuideAssetPresentation)
	for rows.Next() {
		var id, status string
		var width, height sql.NullInt64
		if err := rows.Scan(&id, &status, &width, &height); err != nil {
			return nil, errors.New("could not read Edit Guide Asset metadata")
		}
		presentation, exists := base[id]
		if !exists || status != "READY" {
			continue
		}
		if _, ok := allowed[id][presentation.MediaType]; !ok {
			continue
		}
		var widthValue, heightValue *int
		if width.Valid {
			value := int(width.Int64)
			widthValue = &value
		}
		if height.Valid {
			value := int(height.Int64)
			heightValue = &value
		}
		result[id] = EditGuideAssetPresentation{
			DisplayName: presentation.DisplayName,
			MediaType:   presentation.MediaType,
			Width:       widthValue,
			Height:      heightValue,
			Previewable: presentation.MediaType != "IMAGE" || widthValue != nil && *widthValue > 0 && heightValue != nil && *heightValue > 0,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read Edit Guide Asset metadata")
	}
	return result, nil
}

func queryAssetPresentations(ctx context.Context, queryer contentReadQueryer, workspaceID string, ids map[string]struct{}) (map[string]ContentAssetPresentation, error) {
	result := make(map[string]ContentAssetPresentation)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders, args := uuidPlaceholders(workspaceID, ids)
	rows, err := queryer.QueryContext(ctx, `SELECT id::text, display_name, media_type FROM assets WHERE workspace_id = $1::uuid AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, errors.New("could not load Content Asset presentations")
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var presentation ContentAssetPresentation
		if err := rows.Scan(&id, &presentation.DisplayName, &presentation.MediaType); err != nil {
			return nil, errors.New("could not read Content Asset presentations")
		}
		result[id] = presentation
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read Content Asset presentations")
	}
	return result, nil
}

func uuidPlaceholders(workspaceID string, ids map[string]struct{}) (string, []any) {
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	// Asset IDs are collected from a map; sorting makes query order stable.
	sort.Strings(ordered)
	args := make([]any, 0, len(ordered)+1)
	args = append(args, workspaceID)
	placeholders := make([]string, 0, len(ordered))
	for index, id := range ordered {
		args = append(args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d::uuid", index+2))
	}
	return strings.Join(placeholders, ","), args
}

func validContentMetadata(language, format string) bool {
	return (language == "en" || language == "fa") && (format == "SHORT_VIDEO" || format == "LONG_VIDEO")
}

func validContentVersionSource(source string) bool {
	return source == "AI_GENERATED" || source == "LEGACY_DRAFT_CHECKPOINT" || source == "CREATOR_ACCEPTED"
}

func hasAcceptedVersion(versions []ContentVersionView, acceptedID string) bool {
	for _, version := range versions {
		if version.ID == acceptedID {
			return version.Source == "CREATOR_ACCEPTED"
		}
	}
	return false
}

func unavailableProductionView(view ProductionView) ProductionView {
	view.Status = "UNAVAILABLE"
	view.AcceptedVersionID = nil
	view.AcceptedVersionNumber = nil
	view.ScriptBlocks = nil
	view.AssetPresentations = nil
	return view
}
