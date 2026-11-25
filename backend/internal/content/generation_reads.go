package content

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bettercontent/backend/internal/workspace"
)

type ContentGenerationAttemptHistory struct {
	ID                 string     `json:"id"`
	Status             string     `json:"status"`
	ErrorCategory      *string    `json:"errorCategory"`
	RateLimitSource    *string    `json:"rateLimitSource"`
	RequestedLanguage  string     `json:"requestedLanguage"`
	Format             string     `json:"format"`
	Instructions       *string    `json:"instructions"`
	CreatedAt          time.Time  `json:"createdAt"`
	StartedAt          *time.Time `json:"startedAt"`
	CompletedAt        *time.Time `json:"completedAt"`
	FailedAt           *time.Time `json:"failedAt"`
	ResultingContentID *string    `json:"resultingContentId"`
}

type ContentGenerationAttemptDetail struct {
	Attempt    ContentGenerationAttemptHistory `json:"attempt"`
	SourceIdea ContentSourceIdea               `json:"sourceIdea"`
}

type IdeaContentGenerationHistory struct {
	SourceIdea ContentSourceIdea                 `json:"sourceIdea"`
	IsUsed     bool                              `json:"isUsed"`
	Attempts   []ContentGenerationAttemptHistory `json:"attempts"`
}

type IdeaContentUsage struct {
	IdeaID string `json:"ideaId"`
	IsUsed bool   `json:"isUsed"`
}

type ContentIdeaDetails struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Status      string `json:"status"`
}

type ContentByIdea struct {
	SourceIdea ContentIdeaDetails           `json:"sourceIdea"`
	Content    []ContentDetailView          `json:"content"`
	History    IdeaContentGenerationHistory `json:"history"`
}

type contentAttemptReadRecord struct {
	history ContentGenerationAttemptHistory
	source  ContentSourceIdea
}

func (s *Service) IdeaContentGenerationHistory(ctx context.Context, subject workspace.MembershipSubject, workspaceValue, ideaValue string) (IdeaContentGenerationHistory, error) {
	workspaceID, ideaID, err := parseContentReadTarget(workspaceValue, ideaValue)
	if err != nil {
		return IdeaContentGenerationHistory{}, err
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return IdeaContentGenerationHistory{}, err
	}
	if err := s.recoverStaleForRead(ctx, subject, workspaceID); err != nil {
		return IdeaContentGenerationHistory{}, err
	}
	source, err := loadIdeaSource(ctx, s.database, workspaceID.String(), ideaID)
	if err != nil {
		return IdeaContentGenerationHistory{}, err
	}
	attempts, err := listContentAttempts(ctx, s.database, workspaceID.String(), ideaID)
	if err != nil {
		return IdeaContentGenerationHistory{}, err
	}
	isUsed, err := ideaHasContent(ctx, s.database, workspaceID.String(), ideaID)
	if err != nil {
		return IdeaContentGenerationHistory{}, err
	}
	history := make([]ContentGenerationAttemptHistory, len(attempts))
	for index := range attempts {
		history[index] = attempts[index].history
	}
	return IdeaContentGenerationHistory{SourceIdea: source, IsUsed: isUsed, Attempts: history}, nil
}

func (s *Service) ContentGenerationAttemptDetail(ctx context.Context, subject workspace.MembershipSubject, workspaceValue, attemptValue string) (ContentGenerationAttemptDetail, error) {
	workspaceID, err := workspace.ParseID(workspaceValue)
	if err != nil {
		return ContentGenerationAttemptDetail{}, ErrGenerationValidation
	}
	attemptID, err := parseContentUUID(attemptValue)
	if err != nil {
		return ContentGenerationAttemptDetail{}, ErrGenerationValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return ContentGenerationAttemptDetail{}, err
	}
	if err := s.recoverStaleForRead(ctx, subject, workspaceID); err != nil {
		return ContentGenerationAttemptDetail{}, err
	}
	record, err := loadContentAttempt(ctx, s.database, workspaceID.String(), attemptID)
	if err != nil {
		return ContentGenerationAttemptDetail{}, err
	}
	return ContentGenerationAttemptDetail{Attempt: record.history, SourceIdea: record.source}, nil
}

func (s *Service) ContentGenerationAttemptResult(ctx context.Context, subject workspace.MembershipSubject, workspaceValue, attemptValue string) (*ContentDetailView, error) {
	detail, err := s.ContentGenerationAttemptDetail(ctx, subject, workspaceValue, attemptValue)
	if err != nil {
		return nil, err
	}
	if detail.Attempt.ResultingContentID == nil {
		return nil, nil
	}
	result, err := s.Detail(ctx, subject, ContentTarget{WorkspaceID: workspaceValue, ContentID: *detail.Attempt.ResultingContentID})
	if err != nil {
		return nil, errors.New("resulting Content detail could not be loaded")
	}
	return &result, nil
}

func (s *Service) IdeaContentUsage(ctx context.Context, subject workspace.MembershipSubject, workspaceValue, ideaValue string) (IdeaContentUsage, error) {
	workspaceID, ideaID, err := parseContentReadTarget(workspaceValue, ideaValue)
	if err != nil {
		return IdeaContentUsage{}, err
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return IdeaContentUsage{}, err
	}
	if _, err := loadIdeaSource(ctx, s.database, workspaceID.String(), ideaID); err != nil {
		return IdeaContentUsage{}, err
	}
	isUsed, err := ideaHasContent(ctx, s.database, workspaceID.String(), ideaID)
	if err != nil {
		return IdeaContentUsage{}, err
	}
	return IdeaContentUsage{IdeaID: ideaID, IsUsed: isUsed}, nil
}

func (s *Service) ContentByIdea(ctx context.Context, subject workspace.MembershipSubject, workspaceValue, ideaValue string) (ContentByIdea, error) {
	workspaceID, ideaID, err := parseContentReadTarget(workspaceValue, ideaValue)
	if err != nil {
		return ContentByIdea{}, err
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return ContentByIdea{}, err
	}
	if err := s.recoverStaleForRead(ctx, subject, workspaceID); err != nil {
		return ContentByIdea{}, err
	}
	var idea ContentIdeaDetails
	err = s.database.QueryRowContext(ctx, `
		SELECT i.id::text, i.title, i.description, i.language, i.status
		FROM ideas i
		JOIN idea_generation_batches b ON b.id = i.batch_id AND b.workspace_id = $1::uuid
		WHERE i.id = $2::uuid`, workspaceID.String(), ideaID).Scan(&idea.ID, &idea.Title, &idea.Description, &idea.Language, &idea.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentByIdea{}, ErrGenerationNotFound
	}
	if err != nil {
		return ContentByIdea{}, errors.New("could not load source Idea for Content")
	}
	if (idea.Language != "en" && idea.Language != "fa") || (idea.Status != "NEW" && idea.Status != "SAVED" && idea.Status != "ACCEPTED" && idea.Status != "REJECTED") {
		return ContentByIdea{}, errors.New("stored source Idea invariant is invalid")
	}
	rows, err := s.database.QueryContext(ctx, `SELECT c.id::text FROM contents c WHERE c.workspace_id = $1::uuid AND c.source_idea_id = $2::uuid ORDER BY c.created_at DESC, c.id DESC`, workspaceID.String(), ideaID)
	if err != nil {
		return ContentByIdea{}, errors.New("could not list Content for source Idea")
	}
	var contentIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			rows.Close()
			return ContentByIdea{}, errors.New("could not read Content for source Idea")
		}
		contentIDs = append(contentIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ContentByIdea{}, errors.New("could not read Content for source Idea")
	}
	if err := rows.Close(); err != nil {
		return ContentByIdea{}, errors.New("could not close Content for source Idea results")
	}
	contentDetails := make([]ContentDetailView, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		detail, err := s.Detail(ctx, subject, ContentTarget{WorkspaceID: workspaceID.String(), ContentID: contentID})
		if err != nil {
			return ContentByIdea{}, err
		}
		contentDetails = append(contentDetails, detail)
	}
	history, err := s.IdeaContentGenerationHistory(ctx, subject, workspaceID.String(), ideaID)
	if err != nil {
		return ContentByIdea{}, err
	}
	return ContentByIdea{SourceIdea: idea, Content: contentDetails, History: history}, nil
}

func (s *Service) recoverStaleForRead(ctx context.Context, subject workspace.MembershipSubject, workspaceID workspace.WorkspaceID) error {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("could not begin Content generation recovery")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, workspaceID.String()); err != nil {
		return errors.New("could not lock workspace for Content recovery")
	}
	if _, err := workspace.RequireMembership(ctx, tx, subject, workspaceID); err != nil {
		return err
	}
	if err := lockWorkspace(ctx, tx, workspaceID.String()); err != nil {
		return err
	}
	if err := recoverStaleContentGenerations(ctx, tx, workspaceID.String(), s.clock.Now().UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("could not commit Content generation recovery")
	}
	return nil
}

func parseContentReadTarget(workspaceValue, resourceValue string) (workspace.WorkspaceID, string, error) {
	workspaceID, err := workspace.ParseID(workspaceValue)
	if err != nil {
		return workspace.WorkspaceID{}, "", ErrGenerationValidation
	}
	resourceID, err := parseContentUUID(resourceValue)
	if err != nil {
		return workspace.WorkspaceID{}, "", ErrGenerationValidation
	}
	return workspaceID, resourceID, nil
}

func loadIdeaSource(ctx context.Context, queryer contentReadQueryer, workspaceID, ideaID string) (ContentSourceIdea, error) {
	var result ContentSourceIdea
	err := queryer.QueryRowContext(ctx, `
		SELECT i.id::text, i.title
		FROM ideas i
		JOIN idea_generation_batches b ON b.id = i.batch_id AND b.workspace_id = $1::uuid
		WHERE i.id = $2::uuid`, workspaceID, ideaID).Scan(&result.ID, &result.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentSourceIdea{}, ErrGenerationNotFound
	}
	if err != nil {
		return ContentSourceIdea{}, errors.New("could not load source Idea")
	}
	return result, nil
}

func listContentAttempts(ctx context.Context, queryer contentReadQueryer, workspaceID, ideaID string) ([]contentAttemptReadRecord, error) {
	rows, err := queryer.QueryContext(ctx, contentAttemptReadQuery+` WHERE a.workspace_id = $1::uuid AND a.source_idea_id = $2::uuid ORDER BY a.created_at DESC, a.id DESC`, workspaceID, ideaID)
	if err != nil {
		return nil, errors.New("could not list Content generation Attempts")
	}
	defer rows.Close()
	result := make([]contentAttemptReadRecord, 0)
	for rows.Next() {
		record, err := scanContentAttempt(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read Content generation Attempts")
	}
	return result, nil
}

func loadContentAttempt(ctx context.Context, queryer contentReadQueryer, workspaceID, attemptID string) (contentAttemptReadRecord, error) {
	record, err := scanContentAttempt(queryer.QueryRowContext(ctx, contentAttemptReadQuery+` WHERE a.workspace_id = $1::uuid AND a.id = $2::uuid`, workspaceID, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return contentAttemptReadRecord{}, ErrGenerationNotFound
	}
	return record, err
}

const contentAttemptReadQuery = `
	SELECT a.id::text, a.status, a.error_category, a.requested_language, a.format, a.instructions,
	       a.created_at, a.started_at, a.completed_at, a.failed_at, c.id::text, i.id::text, i.title
	FROM content_generation_attempts a
	JOIN ideas i ON i.id = a.source_idea_id
	JOIN idea_generation_batches b ON b.id = i.batch_id AND b.workspace_id = a.workspace_id
	LEFT JOIN contents c ON c.workspace_id = a.workspace_id AND c.source_generation_attempt_id = a.id`

type contentAttemptScanner interface {
	Scan(...any) error
}

func scanContentAttempt(row contentAttemptScanner) (contentAttemptReadRecord, error) {
	var result contentAttemptReadRecord
	var errorCategory, instructions, resultingContentID sql.NullString
	var startedAt, completedAt, failedAt sql.NullTime
	if err := row.Scan(
		&result.history.ID, &result.history.Status, &errorCategory, &result.history.RequestedLanguage, &result.history.Format, &instructions,
		&result.history.CreatedAt, &startedAt, &completedAt, &failedAt, &resultingContentID, &result.source.ID, &result.source.Title,
	); err != nil {
		return contentAttemptReadRecord{}, err
	}
	if result.history.Status != "PENDING" && result.history.Status != "RUNNING" && result.history.Status != "COMPLETED" && result.history.Status != "FAILED" {
		return contentAttemptReadRecord{}, errors.New("stored Content generation status invariant is invalid")
	}
	if !validContentMetadata(result.history.RequestedLanguage, result.history.Format) {
		return contentAttemptReadRecord{}, errors.New("stored Content generation request invariant is invalid")
	}
	if errorCategory.Valid {
		if !validContentFailureCategory(errorCategory.String) {
			return contentAttemptReadRecord{}, errors.New("stored Content generation failure invariant is invalid")
		}
		value := errorCategory.String
		result.history.ErrorCategory = &value
		if value == "RATE_LIMITED" {
			source := "provider"
			result.history.RateLimitSource = &source
		}
	}
	if instructions.Valid {
		value := instructions.String
		result.history.Instructions = &value
	}
	if startedAt.Valid {
		value := startedAt.Time
		result.history.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		result.history.CompletedAt = &value
	}
	if failedAt.Valid {
		value := failedAt.Time
		result.history.FailedAt = &value
	}
	if resultingContentID.Valid {
		value := resultingContentID.String
		result.history.ResultingContentID = &value
	}
	if result.history.Status == "COMPLETED" && result.history.ResultingContentID == nil || result.history.Status != "COMPLETED" && result.history.ResultingContentID != nil {
		return contentAttemptReadRecord{}, errors.New("stored Content generation result invariant is invalid")
	}
	return result, nil
}

func validContentFailureCategory(category string) bool {
	return category == "TIMEOUT" || category == "RATE_LIMITED" || category == "PROVIDER_UNAVAILABLE" || category == "INVALID_OUTPUT" || category == "INTERRUPTED" || category == "UNKNOWN"
}

func ideaHasContent(ctx context.Context, queryer contentReadQueryer, workspaceID, ideaID string) (bool, error) {
	var exists bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM contents c WHERE c.workspace_id = $1::uuid AND c.source_idea_id = $2::uuid)`, workspaceID, ideaID).Scan(&exists)
	if err != nil {
		return false, errors.New("could not determine source Idea Content usage")
	}
	return exists, nil
}
