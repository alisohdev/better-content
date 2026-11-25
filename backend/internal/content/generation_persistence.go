package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/dna"
	"github.com/google/uuid"
)

type generationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const generationPairColumns = `
	a.id::text, a.workspace_id::text, a.source_idea_id::text, a.content_dna_version_id::text,
	a.requested_language, a.format, a.instructions, a.idempotency_key::text, a.request_fingerprint,
	a.ai_run_id::text, a.status, a.error_category, a.created_at, a.started_at, a.completed_at, a.failed_at,
	r.status, r.provider_request_correlation, c.id::text`

func loadGenerationPair(ctx context.Context, queryer generationQueryer, workspaceID, idempotencyKey string, lock bool) (*generationPair, error) {
	query := `SELECT ` + generationPairColumns + ` FROM content_generation_attempts a
		JOIN ai_runs r ON r.workspace_id = a.workspace_id AND r.id = a.ai_run_id
		LEFT JOIN contents c ON c.workspace_id = a.workspace_id AND c.source_generation_attempt_id = a.id
		WHERE a.workspace_id = $1::uuid AND a.idempotency_key = $2::uuid`
	if lock {
		query += ` FOR UPDATE OF a, r`
	}
	pair, err := scanGenerationPair(queryer.QueryRowContext(ctx, query, workspaceID, idempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not load Content generation operation")
	}
	return &pair, nil
}

func loadGenerationPairByAttempt(ctx context.Context, queryer generationQueryer, workspaceID, attemptID string, lock bool) (generationPair, error) {
	query := `SELECT ` + generationPairColumns + ` FROM content_generation_attempts a
		JOIN ai_runs r ON r.workspace_id = a.workspace_id AND r.id = a.ai_run_id
		LEFT JOIN contents c ON c.workspace_id = a.workspace_id AND c.source_generation_attempt_id = a.id
		WHERE a.workspace_id = $1::uuid AND a.id = $2::uuid`
	if lock {
		query += ` FOR UPDATE OF a, r`
	}
	pair, err := scanGenerationPair(queryer.QueryRowContext(ctx, query, workspaceID, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return generationPair{}, ErrGenerationNotFound
	}
	if err != nil {
		return generationPair{}, errors.New("could not load Content generation attempt")
	}
	return pair, nil
}

func scanGenerationPair(row *sql.Row) (generationPair, error) {
	var pair generationPair
	var instructions, errorCategory, providerCorrelation, contentID sql.NullString
	var startedAt, completedAt, failedAt sql.NullTime
	err := row.Scan(
		&pair.attempt.ID, &pair.attempt.WorkspaceID, &pair.attempt.SourceIdeaID, &pair.attempt.ContentDNAVersionID,
		&pair.attempt.RequestedLanguage, &pair.attempt.Format, &instructions, &pair.request.IdempotencyKey, &pair.fingerprint,
		&pair.runID, &pair.attempt.Status, &errorCategory, &pair.attempt.CreatedAt, &startedAt, &completedAt, &failedAt,
		&pair.runStatus, &providerCorrelation, &contentID,
	)
	if err != nil {
		return generationPair{}, err
	}
	pair.request = ContentGenerationInput{
		WorkspaceID:             pair.attempt.WorkspaceID,
		SourceIdeaID:            pair.attempt.SourceIdeaID,
		BaseContentDNAVersionID: pair.attempt.ContentDNAVersionID,
		RequestedLanguage:       pair.attempt.RequestedLanguage,
		Format:                  pair.attempt.Format,
		IdempotencyKey:          pair.request.IdempotencyKey,
	}
	pair.attempt.AIRunID = pair.runID
	if instructions.Valid {
		value := instructions.String
		pair.request.Instructions = &value
		pair.attempt.Instructions = &value
	}
	pair.attempt.RequestFingerprint = pair.fingerprint
	if errorCategory.Valid {
		value := errorCategory.String
		pair.attempt.ErrorCategory = &value
		if value == string(ai.FailureRateLimited) {
			source := "provider"
			pair.attempt.RateLimitSource = &source
		}
	}
	if startedAt.Valid {
		value := startedAt.Time
		pair.attempt.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		pair.attempt.CompletedAt = &value
	}
	if failedAt.Valid {
		value := failedAt.Time
		pair.attempt.FailedAt = &value
	}
	if providerCorrelation.Valid {
		value := providerCorrelation.String
		pair.runCorrelation = &value
	}
	if contentID.Valid {
		value := contentID.String
		pair.contentID = &value
	}
	return pair, nil
}

type generationStart struct {
	pair    generationPair
	started bool
}

func (s *Service) startGenerationInvocation(ctx context.Context, workspaceID, attemptID, runID string) (generationStart, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return generationStart{}, errors.New("could not begin Content generation start")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return generationStart{}, err
	}
	pair, err := loadGenerationPairByAttempt(ctx, tx, workspaceID, attemptID, true)
	if err != nil {
		return generationStart{}, err
	}
	if pair.runID != runID {
		return generationStart{}, ErrGenerationNotFound
	}
	if pair.attempt.Status != "PENDING" || pair.runStatus != "PENDING" {
		if err := tx.Commit(); err != nil {
			return generationStart{}, errors.New("could not finish Content generation replay check")
		}
		return generationStart{pair: pair}, nil
	}
	var reservationID string
	var invokedAt, releasedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT id::text, invoked_at, released_at FROM workspace_content_generation_quota_reservations WHERE workspace_id = $1::uuid AND attempt_id = $2::uuid FOR UPDATE`, workspaceID, attemptID).Scan(&reservationID, &invokedAt, &releasedAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (invokedAt.Valid || releasedAt.Valid)) {
		now := s.clock.Now().UTC()
		if err := failContentGenerationPair(ctx, tx, pair, ai.FailureInterrupted, now, ""); err != nil {
			return generationStart{}, err
		}
		failedPair, err := loadGenerationPairByAttempt(ctx, tx, workspaceID, attemptID, false)
		if err != nil {
			return generationStart{}, err
		}
		if err := tx.Commit(); err != nil {
			return generationStart{}, errors.New("could not commit interrupted Content generation")
		}
		return generationStart{pair: failedPair}, nil
	}
	if err != nil {
		return generationStart{}, errors.New("could not lock Content generation quota reservation")
	}
	startedAt := s.clock.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE ai_runs SET status = 'RUNNING', started_at = $3 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'PENDING'`, workspaceID, runID, startedAt); err != nil {
		return generationStart{}, errors.New("could not start Content AI Run")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE content_generation_attempts SET status = 'RUNNING', started_at = $3 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'PENDING'`, workspaceID, attemptID, startedAt); err != nil {
		return generationStart{}, errors.New("could not start Content generation attempt")
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspace_content_generation_quota_reservations SET invoked_at = $3 WHERE id = $1::uuid AND workspace_id = $2::uuid AND invoked_at IS NULL AND released_at IS NULL`, reservationID, workspaceID, startedAt)
	if err != nil {
		return generationStart{}, errors.New("could not consume Content generation quota reservation")
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return generationStart{}, errors.New("Content generation quota reservation changed before invocation")
	}
	updatedPair, err := loadGenerationPairByAttempt(ctx, tx, workspaceID, attemptID, false)
	if err != nil {
		return generationStart{}, err
	}
	if err := tx.Commit(); err != nil {
		return generationStart{}, errors.New("could not commit Content generation start")
	}
	return generationStart{pair: updatedPair, started: true}, nil
}

func (s *Service) loadAcceptedGenerationInputs(ctx context.Context, pair generationPair) (acceptedGenerationInputs, error) {
	var idea ai.SourceIdea
	var category sql.NullString
	var rawDNA []byte
	var currentVersionID string
	err := s.database.QueryRowContext(ctx, `
		SELECT i.title, i.description, i.category, v.payload, d.current_version_id::text
		FROM content_generation_attempts a
		JOIN ideas i ON i.id = a.source_idea_id
		JOIN idea_generation_batches b ON b.workspace_id = a.workspace_id AND b.id = i.batch_id
		JOIN content_dna d ON d.workspace_id = a.workspace_id
		JOIN content_dna_versions v ON v.content_dna_id = d.id AND v.id = a.content_dna_version_id
		WHERE a.workspace_id = $1::uuid AND a.id = $2::uuid AND i.status = 'ACCEPTED'
		AND NOT EXISTS (SELECT 1 FROM contents c WHERE c.workspace_id = a.workspace_id AND c.source_idea_id = i.id)`, pair.attempt.WorkspaceID, pair.attempt.ID).Scan(&idea.Title, &idea.Description, &category, &rawDNA, &currentVersionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && currentVersionID != pair.attempt.ContentDNAVersionID) {
		return acceptedGenerationInputs{}, ErrGenerationConflict
	}
	if err != nil {
		return acceptedGenerationInputs{}, errors.New("could not load accepted Content generation inputs")
	}
	if category.Valid {
		value := category.String
		idea.Category = &value
	}
	payload, err := dna.ParsePayload(rawDNA)
	if err != nil || payload.Readiness() != dna.AIReady || payload.Language == nil || !containsContentLanguage(payload.Language.ContentLanguages, pair.attempt.RequestedLanguage) {
		return acceptedGenerationInputs{}, ErrGenerationConflict
	}
	return acceptedGenerationInputs{idea: idea, dna: payload}, nil
}

func (s *Service) completeGenerationInvocation(ctx context.Context, userID, workspaceID string, pair generationPair, result ai.ContentResult) (generationPair, *string, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return generationPair{}, nil, errors.New("could not begin Content generation completion")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return generationPair{}, nil, err
	}
	current, err := loadGenerationPairByAttempt(ctx, tx, workspaceID, pair.attempt.ID, true)
	if err != nil {
		return generationPair{}, nil, err
	}
	if current.runID != pair.runID || current.attempt.Status != "RUNNING" || current.runStatus != "RUNNING" {
		if err := tx.Commit(); err != nil {
			return generationPair{}, nil, errors.New("could not complete late Content generation check")
		}
		return current, current.contentID, nil
	}
	if result.Output == nil || !result.Output.Valid() {
		return generationPair{}, nil, ErrGenerationInvalidOutput
	}
	canonical := result.Output.Document().JSON()
	createdAt := s.clock.Now().UTC()
	contentID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO contents (id, workspace_id, source_idea_id, content_language, format, source_generation_attempt_id, created_at) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::uuid, $7)`, contentID, workspaceID, current.attempt.SourceIdeaID, current.attempt.RequestedLanguage, current.attempt.Format, current.attempt.ID, createdAt); err != nil {
		return generationPair{}, nil, errors.New("could not create generated Content")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO content_drafts (content_id, document, revision, created_at, updated_at) VALUES ($1::uuid, $2::jsonb, 1, $3, $3)`, contentID, canonical, createdAt); err != nil {
		return generationPair{}, nil, errors.New("could not create generated Content Draft")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO content_versions (content_id, version_number, document, source, ai_run_id, created_by_user_id, created_at) VALUES ($1::uuid, 1, $2::jsonb, 'AI_GENERATED', $3::uuid, $4, $5)`, contentID, canonical, current.runID, userID, createdAt); err != nil {
		return generationPair{}, nil, errors.New("could not create initial AI-generated Content Version")
	}
	var usage any
	if result.Usage != nil {
		usageJSON, err := json.Marshal(result.Usage)
		if err != nil {
			return generationPair{}, nil, errors.New("could not encode safe Content generation usage")
		}
		usage = usageJSON
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_runs SET status = 'COMPLETED', output_snapshot = $3::jsonb, usage = $4::jsonb, provider_request_correlation = $5, completed_at = $6 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'RUNNING'`, workspaceID, current.runID, canonical, usage, nullableCorrelation(result.ProviderRequestCorrelation), createdAt); err != nil {
		return generationPair{}, nil, errors.New("could not complete Content AI Run")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE content_generation_attempts SET status = 'COMPLETED', completed_at = $3 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'RUNNING'`, workspaceID, current.attempt.ID, createdAt); err != nil {
		return generationPair{}, nil, errors.New("could not complete Content generation attempt")
	}
	if err := ReconcileIdeaQueueAfterDecision(ctx, tx, workspaceID, current.attempt.SourceIdeaID); err != nil {
		return generationPair{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return generationPair{}, nil, errors.New("could not commit Content generation completion")
	}
	completed, err := loadGenerationPairByAttempt(ctx, s.database, workspaceID, current.attempt.ID, false)
	if err != nil {
		return generationPair{}, nil, errors.New("completed Content generation state could not be read")
	}
	id := contentID
	completed.contentID = &id
	return completed, &id, nil
}

func (s *Service) failGenerationInvocation(ctx context.Context, workspaceID string, pair generationPair, category ai.FailureCategory, correlation string) (generationPair, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return generationPair{}, errors.New("could not begin Content generation failure transition")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return generationPair{}, err
	}
	current, err := loadGenerationPairByAttempt(ctx, tx, workspaceID, pair.attempt.ID, true)
	if err != nil {
		return generationPair{}, err
	}
	if current.attempt.Status == "PENDING" || current.attempt.Status == "RUNNING" {
		if err := failContentGenerationPair(ctx, tx, current, category, s.clock.Now().UTC(), correlation); err != nil {
			return generationPair{}, err
		}
	}
	failed, err := loadGenerationPairByAttempt(ctx, tx, workspaceID, pair.attempt.ID, false)
	if err != nil {
		return generationPair{}, err
	}
	if err := tx.Commit(); err != nil {
		return generationPair{}, errors.New("could not commit Content generation failure transition")
	}
	return failed, nil
}

func failContentGenerationPair(ctx context.Context, tx *sql.Tx, pair generationPair, category ai.FailureCategory, failedAt time.Time, correlation string) error {
	if pair.attempt.Status != pair.runStatus {
		return errors.New("Content generation Attempt and AI Run lifecycle is inconsistent")
	}
	if pair.attempt.Status != "PENDING" && pair.attempt.Status != "RUNNING" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_runs SET status = 'FAILED', error_category = $3, provider_request_correlation = COALESCE($4, provider_request_correlation), failed_at = $5 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = $6`, pair.attempt.WorkspaceID, pair.runID, string(category), nullableCorrelation(correlation), failedAt, pair.runStatus); err != nil {
		return errors.New("could not fail Content AI Run")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE content_generation_attempts SET status = 'FAILED', error_category = $3, failed_at = $4 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = $5`, pair.attempt.WorkspaceID, pair.attempt.ID, string(category), failedAt, pair.attempt.Status); err != nil {
		return errors.New("could not fail Content generation Attempt")
	}
	if pair.attempt.Status == "PENDING" {
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_content_generation_quota_reservations SET released_at = $3 WHERE workspace_id = $1::uuid AND attempt_id = $2::uuid AND invoked_at IS NULL AND released_at IS NULL`, pair.attempt.WorkspaceID, pair.attempt.ID, failedAt); err != nil {
			return errors.New("could not release uninvoked Content quota reservation")
		}
	}
	if strings.TrimSpace(correlation) != "" {
		pair.runCorrelation = &correlation
	}
	return nil
}

func recoverStaleContentGenerations(ctx context.Context, tx *sql.Tx, workspaceID string, now time.Time) error {
	cutoff := now.Add(-contentGenerationStaleAge)
	rows, err := tx.QueryContext(ctx, `SELECT a.id::text FROM content_generation_attempts a JOIN ai_runs r ON r.workspace_id = a.workspace_id AND r.id = a.ai_run_id WHERE a.workspace_id = $1::uuid AND ((a.status = 'PENDING' AND r.status = 'PENDING' AND a.created_at <= $2) OR (a.status = 'RUNNING' AND r.status = 'RUNNING' AND a.started_at <= $2) OR a.status <> r.status) FOR UPDATE OF a, r`, workspaceID, cutoff)
	if err != nil {
		return errors.New("could not find stale Content generations")
	}
	var attemptIDs []string
	for rows.Next() {
		var attemptID string
		if err := rows.Scan(&attemptID); err != nil {
			rows.Close()
			return errors.New("could not read stale Content generations")
		}
		attemptIDs = append(attemptIDs, attemptID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("could not read stale Content generations")
	}
	if err := rows.Close(); err != nil {
		return errors.New("could not close stale Content generation results")
	}
	for _, attemptID := range attemptIDs {
		pair, err := loadGenerationPairByAttempt(ctx, tx, workspaceID, attemptID, true)
		if err != nil {
			return errors.New("stale Content generation pair disappeared")
		}
		if pair.attempt.Status != pair.runStatus {
			return errors.New("Content generation lifecycle invariant is inconsistent")
		}
		if err := failContentGenerationPair(ctx, tx, pair, ai.FailureInterrupted, now, ""); err != nil {
			return err
		}
	}
	return nil
}

func nullableCorrelation(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Service) currentGenerationResult(_ context.Context, pair generationPair, replayed bool) ContentGenerationResult {
	return ContentGenerationResult{Attempt: pair.attempt, ContentID: pair.contentID, Replayed: replayed}
}
