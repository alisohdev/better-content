package ideas

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

type IdeaDTO struct {
	ID                      string    `json:"id"`
	BatchID                 string    `json:"batchId"`
	Position                int       `json:"position"`
	Title                   string    `json:"title"`
	Description             string    `json:"description"`
	Category                *string   `json:"category"`
	Language                string    `json:"language"`
	Status                  string    `json:"status"`
	RejectionReason         *string   `json:"rejectionReason"`
	StatusChangedAt         time.Time `json:"statusChangedAt"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
	ProductionQueuePosition *int      `json:"-"`
}

type BatchHistoryDTO struct {
	ID                      string     `json:"id"`
	ContentDNAVersionID     string     `json:"contentDnaVersionId"`
	ContentDNAVersionNumber int        `json:"contentDnaVersionNumber"`
	RequestedLanguage       string     `json:"requestedLanguage"`
	RequestedCount          int        `json:"requestedCount"`
	Status                  string     `json:"status"`
	ErrorCategory           *string    `json:"errorCategory"`
	RateLimitSource         *string    `json:"rateLimitSource"`
	IdeaCount               int        `json:"ideaCount"`
	CreatedAt               time.Time  `json:"createdAt"`
	StartedAt               *time.Time `json:"startedAt"`
	CompletedAt             *time.Time `json:"completedAt"`
	FailedAt                *time.Time `json:"failedAt"`
}

type BatchHistoryResult struct {
	Batches         []BatchHistoryDTO `json:"batches"`
	SelectedBatchID *string           `json:"selectedBatchId"`
}

type BatchDetailDTO struct {
	BatchHistoryDTO
	Ideas    []IdeaDTO `json:"ideas"`
	CanRetry bool      `json:"canRetry"`
}

type GenerationBatchDTO struct {
	ID                  string     `json:"id"`
	AIRunID             string     `json:"aiRunId"`
	ContentDNAVersionID string     `json:"contentDnaVersionId"`
	RequestedLanguage   string     `json:"requestedLanguage"`
	RequestedCount      int        `json:"requestedCount"`
	Status              string     `json:"status"`
	ErrorCategory       *string    `json:"errorCategory"`
	RateLimitSource     *string    `json:"rateLimitSource"`
	CreatedAt           time.Time  `json:"createdAt"`
	StartedAt           *time.Time `json:"startedAt"`
	CompletedAt         *time.Time `json:"completedAt"`
	FailedAt            *time.Time `json:"failedAt"`
}

type GenerationResultDTO struct {
	Batch    GenerationBatchDTO `json:"batch"`
	Replayed bool               `json:"replayed"`
}

type LibraryItemDTO struct {
	IdeaDTO
	ContentCount int `json:"contentCount"`
	Batch        struct {
		ID                      string    `json:"id"`
		ContentDNAVersionNumber int       `json:"contentDnaVersionNumber"`
		RequestedLanguage       string    `json:"requestedLanguage"`
		Status                  string    `json:"status"`
		CreatedAt               time.Time `json:"createdAt"`
	} `json:"batch"`
}

type IdeaLibraryDTO struct {
	StatusFilter      string           `json:"statusFilter"`
	GenerationBatchID *string          `json:"generationBatchId"`
	Ideas             []LibraryItemDTO `json:"ideas"`
}

func (s *Service) ListBatchHistory(ctx context.Context, subject workspace.MembershipSubject, workspaceID string) (BatchHistoryResult, error) {
	parsedWorkspace, err := parseUUID(workspaceID)
	if err != nil {
		return BatchHistoryResult{}, ErrValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, mustWorkspaceID(parsedWorkspace)); err != nil {
		return BatchHistoryResult{}, err
	}
	if _, err := s.recoverStale(ctx, parsedWorkspace); err != nil {
		return BatchHistoryResult{}, err
	}
	batches, err := listHistoryRows(ctx, s.database, parsedWorkspace)
	if err != nil {
		return BatchHistoryResult{}, err
	}
	var selected *string
	if len(batches) > 0 {
		selected = &batches[0].ID
		if batches[0].Status == "COMPLETED" {
			for i := range batches {
				if batches[i].Status == "COMPLETED" {
					selected = &batches[i].ID
					break
				}
			}
		}
	}
	return BatchHistoryResult{Batches: batches, SelectedBatchID: selected}, nil
}

func (s *Service) GetBatchDetail(ctx context.Context, subject workspace.MembershipSubject, workspaceID, batchID string) (BatchDetailDTO, error) {
	workspaceValue, err := parseUUID(workspaceID)
	if err != nil {
		return BatchDetailDTO{}, ErrValidation
	}
	batchValue, err := parseUUID(batchID)
	if err != nil {
		return BatchDetailDTO{}, ErrValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, mustWorkspaceID(workspaceValue)); err != nil {
		return BatchDetailDTO{}, err
	}
	if _, err := s.recoverStale(ctx, workspaceValue); err != nil {
		return BatchDetailDTO{}, err
	}
	rows, err := listHistoryRows(ctx, s.database, workspaceValue, batchValue)
	if err != nil {
		return BatchDetailDTO{}, err
	}
	if len(rows) == 0 {
		return BatchDetailDTO{}, ErrNotFound
	}
	detail := BatchDetailDTO{BatchHistoryDTO: rows[0], Ideas: []IdeaDTO{}, CanRetry: rows[0].Status == "FAILED"}
	ideaRows, err := s.database.QueryContext(ctx, `SELECT id::text, batch_id::text, position, title, description, category, language, status, rejection_reason, status_changed_at, created_at, updated_at, production_queue_position FROM ideas WHERE batch_id = $1::uuid ORDER BY position`, batchValue)
	if err != nil {
		return BatchDetailDTO{}, errors.New("could not load idea batch detail")
	}
	defer ideaRows.Close()
	for ideaRows.Next() {
		idea, scanErr := scanIdea(ideaRows)
		if scanErr != nil {
			return BatchDetailDTO{}, errors.New("could not read idea batch detail")
		}
		detail.Ideas = append(detail.Ideas, idea)
	}
	if err := ideaRows.Err(); err != nil {
		return BatchDetailDTO{}, errors.New("could not read idea batch detail")
	}
	if detail.Status == "COMPLETED" && detail.IdeaCount != ai.IdeaGenerationCount {
		return BatchDetailDTO{}, errors.New("completed idea generation count invariant failed")
	}
	if detail.Status != "COMPLETED" {
		detail.Ideas = []IdeaDTO{}
	}
	return detail, nil
}

func (s *Service) GetIdeaLibrary(ctx context.Context, subject workspace.MembershipSubject, workspaceID, statusFilter string, batchID *string) (IdeaLibraryDTO, error) {
	workspaceValue, err := parseUUID(workspaceID)
	if err != nil {
		return IdeaLibraryDTO{}, ErrValidation
	}
	if statusFilter == "" {
		statusFilter = "NEW"
	}
	if statusFilter != "ALL" && statusFilter != "NEW" && statusFilter != "SAVED" && statusFilter != "ACCEPTED" && statusFilter != "REJECTED" {
		return IdeaLibraryDTO{}, ErrValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, mustWorkspaceID(workspaceValue)); err != nil {
		return IdeaLibraryDTO{}, err
	}
	var ownedBatchID *string
	if batchID != nil {
		parsedBatch, parseErr := parseUUID(*batchID)
		if parseErr == nil {
			var owned string
			err := s.database.QueryRowContext(ctx, `SELECT id::text FROM idea_generation_batches WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceValue, parsedBatch).Scan(&owned)
			if err == nil {
				ownedBatchID = &owned
			} else if !errors.Is(err, sql.ErrNoRows) {
				return IdeaLibraryDTO{}, errors.New("could not validate idea generation batch")
			}
		}
	}
	query := `SELECT i.id::text, i.batch_id::text, i.position, i.title, i.description, i.category, i.language, i.status, i.rejection_reason, i.status_changed_at, i.created_at, i.updated_at, i.production_queue_position, b.id::text, v.version_number, b.requested_language, b.status, b.created_at, count(c.id) FROM ideas i JOIN idea_generation_batches b ON b.id = i.batch_id JOIN content_dna_versions v ON v.id = b.content_dna_version_id LEFT JOIN contents c ON c.source_idea_id = i.id AND c.workspace_id = b.workspace_id WHERE b.workspace_id = $1::uuid`
	args := []any{workspaceValue}
	if statusFilter != "ALL" {
		args = append(args, statusFilter)
		query += ` AND i.status = $` + itoa(len(args))
	}
	if ownedBatchID != nil {
		args = append(args, *ownedBatchID)
		query += ` AND b.id = $` + itoa(len(args)) + `::uuid`
	}
	query += ` GROUP BY i.id, b.id, v.id ORDER BY b.created_at DESC, b.id DESC, i.position ASC`
	rows, err := s.database.QueryContext(ctx, query, args...)
	if err != nil {
		return IdeaLibraryDTO{}, errors.New("could not load Idea Library")
	}
	defer rows.Close()
	result := IdeaLibraryDTO{StatusFilter: statusFilter, GenerationBatchID: ownedBatchID, Ideas: []LibraryItemDTO{}}
	for rows.Next() {
		var item LibraryItemDTO
		var versionNumber, contentCount int
		if err := rows.Scan(&item.ID, &item.BatchID, &item.Position, &item.Title, &item.Description, &item.Category, &item.Language, &item.Status, &item.RejectionReason, &item.StatusChangedAt, &item.CreatedAt, &item.UpdatedAt, &item.ProductionQueuePosition, &item.Batch.ID, &versionNumber, &item.Batch.RequestedLanguage, &item.Batch.Status, &item.Batch.CreatedAt, &contentCount); err != nil {
			return IdeaLibraryDTO{}, errors.New("could not read Idea Library")
		}
		item.ContentCount = contentCount
		item.Batch.ContentDNAVersionNumber = versionNumber
		if !validIdeaState(item.Status) || (item.Language != "en" && item.Language != "fa") || !validLifecycle(item.Batch.Status) {
			return IdeaLibraryDTO{}, errors.New("stored Idea Library state is invalid")
		}
		result.Ideas = append(result.Ideas, item)
	}
	if err := rows.Err(); err != nil {
		return IdeaLibraryDTO{}, errors.New("could not read Idea Library")
	}
	return result, nil
}

func (s *Service) RetryBatch(ctx context.Context, subject workspace.MembershipSubject, workspaceID, batchID string) (GenerationResult, error) {
	workspaceValue, err := parseUUID(workspaceID)
	if err != nil {
		return GenerationResult{}, ErrValidation
	}
	batchValue, err := parseUUID(batchID)
	if err != nil {
		return GenerationResult{}, ErrValidation
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, mustWorkspaceID(workspaceValue)); err != nil {
		return GenerationResult{}, err
	}
	if _, err := s.recoverStale(ctx, workspaceValue); err != nil {
		return GenerationResult{}, err
	}
	var status, language string
	err = s.database.QueryRowContext(ctx, `SELECT status, requested_language FROM idea_generation_batches WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceValue, batchValue).Scan(&status, &language)
	if errors.Is(err, sql.ErrNoRows) {
		return GenerationResult{}, ErrNotFound
	}
	if err != nil {
		return GenerationResult{}, errors.New("could not load Idea generation retry")
	}
	if status != "FAILED" {
		return GenerationResult{}, ErrConflict
	}
	var currentDNAID string
	var raw []byte
	err = s.database.QueryRowContext(ctx, `SELECT v.id::text, v.payload FROM content_dna c JOIN content_dna_versions v ON v.content_dna_id = c.id AND v.id = c.current_version_id WHERE c.workspace_id = $1::uuid`, workspaceValue).Scan(&currentDNAID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return GenerationResult{}, ErrValidation
	}
	if err != nil {
		return GenerationResult{}, errors.New("could not load current Content DNA for retry")
	}
	payload, err := dna.ParsePayload(raw)
	if err != nil || payload.Readiness() != dna.AIReady || payload.Language == nil || !contains(payload.Language.ContentLanguages, language) {
		return GenerationResult{}, ErrValidation
	}
	return s.Generate(ctx, subject, GenerationInput{WorkspaceID: workspaceValue, BaseContentDNAVersionID: currentDNAID, RequestedLanguage: language, IdempotencyKey: uuid.NewString()})
}

func ToGenerationResultDTO(result GenerationResult) GenerationResultDTO {
	batch := result.Batch
	dto := GenerationBatchDTO{
		ID: batch.ID, AIRunID: batch.AIRunID, ContentDNAVersionID: batch.ContentDNAVersionID,
		RequestedLanguage: batch.RequestedLanguage, RequestedCount: batch.RequestedCount,
		Status: batch.Status, ErrorCategory: batch.ErrorCategory,
		CreatedAt: batch.CreatedAt, StartedAt: batch.StartedAt, CompletedAt: batch.CompletedAt, FailedAt: batch.FailedAt,
	}
	if batch.Status == "FAILED" && batch.ErrorCategory != nil && *batch.ErrorCategory == string(ai.FailureRateLimited) {
		provider := "provider"
		dto.RateLimitSource = &provider
	}
	return GenerationResultDTO{Batch: dto, Replayed: result.Replayed}
}

func (s *Service) recoverStale(ctx context.Context, workspaceID string) (int, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.New("could not begin stale generation recovery")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, workspaceID); err != nil {
		return 0, errors.New("could not serialize stale generation recovery")
	}
	recovered, err := recoverStaleInTransaction(ctx, tx, workspaceID, s.clock.Now().UTC())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.New("could not commit stale generation recovery")
	}
	return recovered, nil
}

func listHistoryRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, workspaceID string, batchIDs ...string) ([]BatchHistoryDTO, error) {
	query := `SELECT b.id::text, b.content_dna_version_id::text, v.version_number, b.requested_language, b.requested_count, b.status, b.error_category, count(i.id), b.created_at, b.started_at, b.completed_at, b.failed_at FROM idea_generation_batches b JOIN content_dna_versions v ON v.id = b.content_dna_version_id LEFT JOIN ideas i ON i.batch_id = b.id WHERE b.workspace_id = $1::uuid`
	args := []any{workspaceID}
	if len(batchIDs) > 0 {
		args = append(args, batchIDs[0])
		query += ` AND b.id = $2::uuid`
	}
	query += ` GROUP BY b.id, v.id ORDER BY b.created_at DESC, b.id DESC`
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.New("could not load idea generation history")
	}
	defer rows.Close()
	result := []BatchHistoryDTO{}
	for rows.Next() {
		var item BatchHistoryDTO
		var errorCategory sql.NullString
		if err := rows.Scan(&item.ID, &item.ContentDNAVersionID, &item.ContentDNAVersionNumber, &item.RequestedLanguage, &item.RequestedCount, &item.Status, &errorCategory, &item.IdeaCount, &item.CreatedAt, &item.StartedAt, &item.CompletedAt, &item.FailedAt); err != nil {
			return nil, errors.New("could not read idea generation history")
		}
		if errorCategory.Valid {
			item.ErrorCategory = &errorCategory.String
		}
		if item.Status == "FAILED" && errorCategory.Valid && errorCategory.String == string(ai.FailureRateLimited) {
			provider := "provider"
			item.RateLimitSource = &provider
		}
		if !validLifecycle(item.Status) || item.RequestedCount != ai.IdeaGenerationCount || (item.RequestedLanguage != "en" && item.RequestedLanguage != "fa") {
			return nil, errors.New("stored idea generation history state is invalid")
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read idea generation history")
	}
	return result, nil
}

type ideaRowScanner interface{ Scan(...any) error }

func scanIdea(row ideaRowScanner) (IdeaDTO, error) {
	var item IdeaDTO
	if err := row.Scan(&item.ID, &item.BatchID, &item.Position, &item.Title, &item.Description, &item.Category, &item.Language, &item.Status, &item.RejectionReason, &item.StatusChangedAt, &item.CreatedAt, &item.UpdatedAt, &item.ProductionQueuePosition); err != nil {
		return IdeaDTO{}, err
	}
	if !validIdeaState(item.Status) || (item.Language != "en" && item.Language != "fa") {
		return IdeaDTO{}, errors.New("stored Idea state is invalid")
	}
	return item, nil
}

func validLifecycle(value string) bool {
	return value == "PENDING" || value == "RUNNING" || value == "COMPLETED" || value == "FAILED"
}
func validIdeaState(value string) bool {
	return value == "NEW" || value == "SAVED" || value == "ACCEPTED" || value == "REJECTED"
}
func itoa(value int) string { return strconv.Itoa(value) }
