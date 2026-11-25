package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

const (
	assetLibraryPageSize = 24
	maxAssetLibraryPage  = 100
)

type AssetLibraryInput struct {
	WorkspaceID string
	Page        int
	Search      string
	MediaType   *string
	MediaTypes  []string
	AssetIDs    []string
	Status      *string
}

type AssetLibraryItem struct {
	ID             string    `json:"id"`
	MediaType      string    `json:"mediaType"`
	SourceType     string    `json:"sourceType"`
	Status         string    `json:"status"`
	DisplayName    string    `json:"displayName"`
	SourceHost     *string   `json:"sourceHost"`
	ByteSize       *int64    `json:"byteSize"`
	Width          *int      `json:"width"`
	Height         *int      `json:"height"`
	DurationMS     *int      `json:"durationMs"`
	MediaFormat    *string   `json:"mediaFormat"`
	FailureCode    *string   `json:"failureCode"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	ReferenceCount int       `json:"referenceCount"`
}

type AssetLibraryResult struct {
	Page        int                `json:"page"`
	PageSize    int                `json:"pageSize"`
	HasNextPage bool               `json:"hasNextPage"`
	Search      string             `json:"search"`
	MediaType   *string            `json:"mediaType"`
	Status      *string            `json:"status"`
	Assets      []AssetLibraryItem `json:"assets"`
}

type AssetRenameInput struct {
	WorkspaceID string
	AssetID     string
	DisplayName string
}

var (
	ErrAssetLibraryInvalid = errors.New("Asset Library request is invalid")
	ErrAssetConflict       = errors.New("Asset cannot be changed in its current state")
)

func (s *Service) GetAssetLibrary(ctx context.Context, subject workspace.MembershipSubject, input AssetLibraryInput) (AssetLibraryResult, error) {
	userID, authenticated := authenticatedUserID(subject)
	if !authenticated || userID == "" {
		return AssetLibraryResult{}, ErrUnauthenticated
	}
	workspaceID, err := workspace.ParseID(input.WorkspaceID)
	if err != nil {
		return AssetLibraryResult{}, ErrAssetLibraryInvalid
	}
	query, err := normalizeAssetLibraryInput(input)
	if err != nil {
		return AssetLibraryResult{}, err
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return AssetLibraryResult{}, err
	}
	items, err := listAssetLibraryItems(ctx, s.database, workspaceID.String(), query)
	if err != nil {
		return AssetLibraryResult{}, err
	}
	hasNextPage := len(items) > assetLibraryPageSize
	if hasNextPage {
		items = items[:assetLibraryPageSize]
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "assets.library.loaded", "module", "assets", "operation", "assetLibrary", "workspaceId", workspaceID.String(), "userId", userID)
	}
	return AssetLibraryResult{
		Page: query.page, PageSize: assetLibraryPageSize, HasNextPage: hasNextPage,
		Search: query.search, MediaType: query.mediaType, Status: query.status, Assets: items,
	}, nil
}

func (s *Service) RenameAsset(ctx context.Context, subject workspace.MembershipSubject, input AssetRenameInput) (AssetLibraryItem, error) {
	userID, authenticated := authenticatedUserID(subject)
	if !authenticated || userID == "" {
		return AssetLibraryItem{}, ErrUnauthenticated
	}
	workspaceID, err := workspace.ParseID(input.WorkspaceID)
	if err != nil {
		return AssetLibraryItem{}, ErrAssetLibraryInvalid
	}
	assetID, err := uuid.Parse(input.AssetID)
	if err != nil || assetID == uuid.Nil {
		return AssetLibraryItem{}, ErrAssetLibraryInvalid
	}
	displayName, err := normalizeSafeName(input.DisplayName, 200)
	if err != nil {
		return AssetLibraryItem{}, ErrAssetLibraryInvalid
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return AssetLibraryItem{}, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return AssetLibraryItem{}, errors.New("could not begin Asset rename")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return AssetLibraryItem{}, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return AssetLibraryItem{}, err
	}
	current, err := readRenameAsset(ctx, tx, workspaceID.String(), assetID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return AssetLibraryItem{}, ErrAssetNotFound
	}
	if err != nil {
		return AssetLibraryItem{}, err
	}
	if current.Status == "DELETING" {
		return AssetLibraryItem{}, ErrAssetConflict
	}
	current.DisplayName = string(displayName)
	current.UpdatedAt = s.clock().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE assets SET display_name = $3, updated_at = $4 WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID.String(), assetID.String(), current.DisplayName, current.UpdatedAt); err != nil {
		return AssetLibraryItem{}, errors.New("could not rename Asset")
	}
	if err := tx.Commit(); err != nil {
		return AssetLibraryItem{}, errors.New("could not commit Asset rename")
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "assets.library.renamed", "module", "assets", "operation", "renameAsset", "workspaceId", workspaceID.String(), "userId", userID, "entityId", current.ID)
	}
	return current, nil
}

type normalizedAssetLibraryInput struct {
	page       int
	search     string
	mediaType  *string
	mediaTypes []string
	assetIDs   []string
	status     *string
}

func normalizeAssetLibraryInput(input AssetLibraryInput) (normalizedAssetLibraryInput, error) {
	page := input.Page
	if page == 0 {
		page = 1
	}
	search := strings.TrimSpace(input.Search)
	if page < 1 || page > maxAssetLibraryPage || utf8.RuneCountInString(input.Search) > 400 || utf8.RuneCountInString(search) > 200 {
		return normalizedAssetLibraryInput{}, ErrAssetLibraryInvalid
	}
	query := normalizedAssetLibraryInput{page: page, search: search}
	if input.MediaType != nil {
		if !validMediaType(*input.MediaType) {
			return normalizedAssetLibraryInput{}, ErrAssetLibraryInvalid
		}
		value := *input.MediaType
		query.mediaType = &value
	}
	if len(input.MediaTypes) > 2 || len(input.MediaTypes) == 1 && input.MediaTypes[0] == "" {
		return normalizedAssetLibraryInput{}, ErrAssetLibraryInvalid
	}
	if len(input.MediaTypes) > 0 {
		seen := make(map[string]struct{}, len(input.MediaTypes))
		for _, value := range input.MediaTypes {
			if !validMediaType(value) {
				return normalizedAssetLibraryInput{}, ErrAssetLibraryInvalid
			}
			if _, exists := seen[value]; !exists {
				query.mediaTypes = append(query.mediaTypes, value)
				seen[value] = struct{}{}
			}
		}
	}
	if len(input.AssetIDs) > 5 {
		return normalizedAssetLibraryInput{}, ErrAssetLibraryInvalid
	}
	if len(input.AssetIDs) > 0 {
		seen := make(map[string]struct{}, len(input.AssetIDs))
		for _, value := range input.AssetIDs {
			id, err := uuid.Parse(value)
			if err != nil || id == uuid.Nil {
				return normalizedAssetLibraryInput{}, ErrAssetLibraryInvalid
			}
			canonical := id.String()
			if _, exists := seen[canonical]; !exists {
				query.assetIDs = append(query.assetIDs, canonical)
				seen[canonical] = struct{}{}
			}
		}
	}
	if input.Status != nil {
		if !validAssetStatus(*input.Status) {
			return normalizedAssetLibraryInput{}, ErrAssetLibraryInvalid
		}
		value := *input.Status
		query.status = &value
	}
	return query, nil
}

func listAssetLibraryItems(ctx context.Context, database *sql.DB, workspaceID string, query normalizedAssetLibraryInput) ([]AssetLibraryItem, error) {
	conditions := []string{"a.workspace_id = $1::uuid"}
	args := []any{workspaceID}
	addCondition := func(expression string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(expression, len(args)))
	}
	if len(query.assetIDs) > 0 {
		placeholders := make([]string, 0, len(query.assetIDs))
		for _, id := range query.assetIDs {
			args = append(args, id)
			placeholders = append(placeholders, fmt.Sprintf("$%d::uuid", len(args)))
		}
		conditions = append(conditions, "a.id IN ("+strings.Join(placeholders, ", ")+")")
	}
	mediaTypes := query.mediaTypes
	if len(mediaTypes) == 0 && query.mediaType != nil {
		mediaTypes = []string{*query.mediaType}
	}
	if len(mediaTypes) == 1 {
		addCondition("a.media_type = $%d", mediaTypes[0])
	} else if len(mediaTypes) > 1 {
		placeholders := make([]string, 0, len(mediaTypes))
		for _, value := range mediaTypes {
			args = append(args, value)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		conditions = append(conditions, "a.media_type IN ("+strings.Join(placeholders, ", ")+")")
	}
	if query.status != nil {
		addCondition("a.status = $%d", *query.status)
	}
	if query.search != "" {
		args = append(args, escapedAssetSearchSubstring(query.search))
		placeholder := fmt.Sprintf("$%d", len(args))
		conditions = append(conditions, "(lower(a.display_name) LIKE lower("+placeholder+") ESCAPE E'\\\\' OR (a.source_type = 'UPLOAD' AND lower(coalesce(a.original_filename, '')) LIKE lower("+placeholder+") ESCAPE E'\\\\'))")
	}
	limit := len(args) + 1
	args = append(args, assetLibraryPageSize+1)
	offset := len(args) + 1
	args = append(args, (query.page-1)*assetLibraryPageSize)
	sqlQuery := `
		SELECT a.id::text, a.media_type, a.source_type, a.status, a.display_name, a.source_host,
			a.byte_size, a.width, a.height, a.duration_ms, a.media_format, a.failure_code,
			a.created_at, a.updated_at,
			(SELECT count(DISTINCT r.content_id)::int FROM asset_references AS r
			 WHERE r.workspace_id = a.workspace_id AND r.asset_id = a.id) AS reference_count
		FROM assets AS a
		WHERE ` + strings.Join(conditions, " AND ") +
		fmt.Sprintf(" ORDER BY a.created_at DESC, a.id DESC LIMIT $%d OFFSET $%d", limit, offset)
	rows, err := database.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, errors.New("could not read Asset Library")
	}
	defer rows.Close()
	items := make([]AssetLibraryItem, 0, assetLibraryPageSize+1)
	for rows.Next() {
		var item AssetLibraryItem
		var sourceHost, mediaFormat, failureCode sql.NullString
		var byteSize sql.NullInt64
		var width, height, durationMS sql.NullInt64
		if err := rows.Scan(&item.ID, &item.MediaType, &item.SourceType, &item.Status, &item.DisplayName,
			&sourceHost, &byteSize, &width, &height, &durationMS, &mediaFormat, &failureCode,
			&item.CreatedAt, &item.UpdatedAt, &item.ReferenceCount); err != nil {
			return nil, errors.New("could not read Asset Library records")
		}
		item.SourceHost = nullableString(sourceHost)
		item.ByteSize = nullableInt64(byteSize)
		item.Width = nullableInt(width)
		item.Height = nullableInt(height)
		item.DurationMS = nullableInt(durationMS)
		item.MediaFormat = nullableString(mediaFormat)
		item.FailureCode = nullableString(failureCode)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read Asset Library records")
	}
	return items, nil
}

func readRenameAsset(ctx context.Context, tx *sql.Tx, workspaceID, assetID string) (AssetLibraryItem, error) {
	var item AssetLibraryItem
	var sourceHost, mediaFormat, failureCode sql.NullString
	var byteSize sql.NullInt64
	var width, height, durationMS sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT id::text, media_type, source_type, status, display_name, source_host,
			byte_size, width, height, duration_ms, media_format, failure_code, created_at, updated_at
		FROM assets WHERE workspace_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, workspaceID, assetID).Scan(&item.ID, &item.MediaType, &item.SourceType, &item.Status, &item.DisplayName,
		&sourceHost, &byteSize, &width, &height, &durationMS, &mediaFormat, &failureCode, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AssetLibraryItem{}, sql.ErrNoRows
		}
		return AssetLibraryItem{}, errors.New("could not read Asset for rename")
	}
	item.SourceHost = nullableString(sourceHost)
	item.ByteSize = nullableInt64(byteSize)
	item.Width = nullableInt(width)
	item.Height = nullableInt(height)
	item.DurationMS = nullableInt(durationMS)
	item.MediaFormat = nullableString(mediaFormat)
	item.FailureCode = nullableString(failureCode)
	return item, nil
}

func escapedAssetSearchSubstring(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	value = strings.ReplaceAll(value, "_", `\_`)
	return "%" + value + "%"
}

func validMediaType(value string) bool {
	return value == "IMAGE" || value == "VIDEO" || value == "AUDIO"
}

func validAssetStatus(value string) bool {
	switch value {
	case "PENDING", "PROCESSING", "READY", "FAILED", "DELETING":
		return true
	default:
		return false
	}
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}
