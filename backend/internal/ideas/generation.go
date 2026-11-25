package ideas

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf16"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/platform/clock"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

const (
	tenMinuteQuota  = 3
	twentyFourQuota = 12
	tenMinutes      = 10 * time.Minute
	twentyFourHours = 24 * time.Hour
	staleAttemptAge = 75 * time.Second
)

var (
	ErrValidation    = errors.New("idea generation request is invalid")
	ErrConflict      = errors.New("idea generation state changed")
	ErrRateLimited   = errors.New("workspace idea generation quota was reached")
	ErrNotFound      = errors.New("idea generation resource was not found")
	ErrInvalidOutput = errors.New("idea provider returned invalid output")
	ErrProvider      = errors.New("idea provider operation failed")
)

type GenerationInput struct {
	WorkspaceID             string `json:"workspaceId"`
	BaseContentDNAVersionID string `json:"baseContentDnaVersionId"`
	RequestedLanguage       string `json:"requestedLanguage"`
	IdempotencyKey          string `json:"idempotencyKey"`
}

type Batch struct {
	ID                  string     `json:"id"`
	WorkspaceID         string     `json:"workspaceId"`
	ContentDNAVersionID string     `json:"contentDnaVersionId"`
	AIRunID             string     `json:"aiRunId"`
	IdempotencyKey      string     `json:"-"`
	RequestFingerprint  string     `json:"-"`
	RequestedLanguage   string     `json:"requestedLanguage"`
	RequestedCount      int        `json:"requestedCount"`
	Status              string     `json:"status"`
	ErrorCategory       *string    `json:"errorCategory"`
	CreatedAt           time.Time  `json:"createdAt"`
	StartedAt           *time.Time `json:"startedAt"`
	CompletedAt         *time.Time `json:"completedAt"`
	FailedAt            *time.Time `json:"failedAt"`
}

type AIRecord struct {
	ID                         string
	Status                     string
	ProviderRequestCorrelation *string
}

type GenerationPair struct {
	Batch Batch
	Run   AIRecord
}

type GenerationResult struct {
	Batch    Batch `json:"batch"`
	Replayed bool  `json:"replayed"`
}

type ProviderFactory func(userID string) (ai.GenerateIdeasProvider, error)

type Service struct {
	database        *sql.DB
	providerFactory ProviderFactory
	clock           clock.Clock
	logger          *slog.Logger
}

func NewService(database *sql.DB, providerFactory ProviderFactory, sourceClock clock.Clock, logger *slog.Logger) *Service {
	if sourceClock == nil {
		sourceClock = clock.System{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{database: database, providerFactory: providerFactory, clock: sourceClock, logger: logger}
}

func (s *Service) Generate(ctx context.Context, subject workspace.MembershipSubject, input GenerationInput) (GenerationResult, error) {
	userID, authenticated := subject.AuthenticatedUserID()
	if !authenticated || userID == "" {
		return GenerationResult{}, workspace.ErrUnauthenticated
	}
	input, err := validateGenerationInput(input)
	if err != nil {
		return GenerationResult{}, err
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, mustWorkspaceID(input.WorkspaceID)); err != nil {
		return GenerationResult{}, err
	}
	fingerprint := fingerprintGeneration(input)
	preflight, err := s.reserve(ctx, subject, input, fingerprint)
	if err != nil {
		return GenerationResult{}, err
	}
	if preflight.replayed {
		return GenerationResult{Batch: preflight.pair.Batch, Replayed: true}, nil
	}
	if preflight.rateLimited {
		return GenerationResult{}, ErrRateLimited
	}
	if s.providerFactory == nil {
		_, failErr := s.fail(ctx, input.WorkspaceID, preflight.pair.Batch.ID, ai.FailureUnknown, "")
		if failErr != nil {
			return GenerationResult{}, failErr
		}
		return GenerationResult{}, ErrProvider
	}
	provider, err := s.providerFactory(userID)
	if err != nil {
		failed, failErr := s.fail(ctx, input.WorkspaceID, preflight.pair.Batch.ID, ai.FailureUnknown, "")
		if failErr != nil {
			return GenerationResult{}, failErr
		}
		return GenerationResult{Batch: failed.Batch}, ErrProvider
	}
	started, err := s.start(ctx, input.WorkspaceID, preflight.pair.Batch.ID)
	if err != nil {
		return GenerationResult{}, err
	}
	if !started.started {
		if started.pair.Batch.Status == "FAILED" {
			return GenerationResult{Batch: started.pair.Batch}, ErrProvider
		}
		return GenerationResult{Batch: started.pair.Batch, Replayed: true}, nil
	}
	providerCtx, cancel := context.WithTimeout(ctx, ai.IdeaGenerationTimeoutSecs*time.Second)
	providerResult := provider.GenerateIdeas(providerCtx, ai.Request{
		GenerationKind:    ai.IdeaGenerationKind,
		ContentDNA:        preflight.contentDNA,
		RequestedLanguage: input.RequestedLanguage,
		RequestedCount:    ai.IdeaGenerationCount,
		PromptVersion:     ai.IdeaGenerationPrompt,
	})
	providerContextErr := providerCtx.Err()
	cancel()
	if errors.Is(providerContextErr, context.DeadlineExceeded) {
		providerResult = ai.Result{ErrorCategory: ai.FailureTimeout}
	}
	if !providerResult.Succeeded() {
		category := providerResult.ErrorCategory
		if category == "" {
			category = ai.FailureUnknown
		}
		failed, failErr := s.fail(ctx, input.WorkspaceID, preflight.pair.Batch.ID, category, providerResult.ProviderRequestCorrelation)
		if failErr != nil {
			return GenerationResult{}, failErr
		}
		return GenerationResult{Batch: failed.Batch}, mapProviderFailure(category)
	}
	canonicalOutput, err := ai.CanonicalizeIdeaOutput(mustMarshalOutput(providerResult.Output))
	if err != nil {
		failed, failErr := s.fail(ctx, input.WorkspaceID, preflight.pair.Batch.ID, ai.FailureInvalidOutput, providerResult.ProviderRequestCorrelation)
		if failErr != nil {
			return GenerationResult{}, failErr
		}
		return GenerationResult{Batch: failed.Batch}, ErrInvalidOutput
	}
	providerResult.Output = &canonicalOutput
	completed, err := s.complete(ctx, input.WorkspaceID, preflight.pair.Batch.ID, providerResult)
	if err != nil {
		return GenerationResult{}, err
	}
	return GenerationResult{Batch: completed.Batch}, nil
}

func (s *Service) reserve(ctx context.Context, subject workspace.MembershipSubject, input GenerationInput, fingerprint string) (preflightResult, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return preflightResult{}, errors.New("could not begin idea generation reservation")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, input.WorkspaceID); err != nil {
		return preflightResult{}, errors.New("could not serialize idea generation")
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, mustWorkspaceID(input.WorkspaceID)); err != nil {
		return preflightResult{}, err
	}
	if existing, err := loadPairByIdempotency(ctx, tx, input.WorkspaceID, input.IdempotencyKey); err != nil {
		return preflightResult{}, err
	} else if existing != nil {
		if existing.Batch.RequestFingerprint != fingerprint {
			return preflightResult{}, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return preflightResult{}, errors.New("could not complete idempotency lookup")
		}
		return preflightResult{pair: *existing, replayed: true}, nil
	}
	now := s.clock.Now().UTC()
	if _, err := recoverStaleInTransaction(ctx, tx, input.WorkspaceID, now); err != nil {
		return preflightResult{}, err
	}
	if existing, err := loadPairByIdempotency(ctx, tx, input.WorkspaceID, input.IdempotencyKey); err != nil {
		return preflightResult{}, err
	} else if existing != nil {
		if existing.Batch.RequestFingerprint != fingerprint {
			return preflightResult{}, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return preflightResult{}, errors.New("could not complete idempotency lookup")
		}
		return preflightResult{pair: *existing, replayed: true}, nil
	}
	contentDNA, err := loadCurrentDNASnapshot(ctx, tx, input.WorkspaceID, input.BaseContentDNAVersionID, input.RequestedLanguage)
	if err != nil {
		return preflightResult{}, err
	}
	tenMinute, err := quotaCounts(ctx, tx, input.WorkspaceID, now.Add(-tenMinutes))
	if err != nil {
		return preflightResult{}, errors.New("could not check workspace idea generation quota")
	}
	day, err := quotaCounts(ctx, tx, input.WorkspaceID, now.Add(-twentyFourHours))
	if err != nil {
		return preflightResult{}, errors.New("could not check workspace idea generation quota")
	}
	if tenMinute.invoked+tenMinute.live >= tenMinuteQuota || day.invoked+day.live >= twentyFourQuota {
		if err := tx.Commit(); err != nil {
			return preflightResult{}, errors.New("could not complete quota check")
		}
		return preflightResult{rateLimited: true}, nil
	}
	pair, err := createGenerationPair(ctx, tx, input, fingerprint, now)
	if err != nil {
		return preflightResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_generation_quota_reservations (workspace_id, batch_id, reserved_at) VALUES ($1::uuid, $2::uuid, $3)`, input.WorkspaceID, pair.Batch.ID, now); err != nil {
		return preflightResult{}, errors.New("could not reserve workspace generation quota")
	}
	if err := tx.Commit(); err != nil {
		return preflightResult{}, errors.New("could not commit idea generation reservation")
	}
	return preflightResult{pair: pair, contentDNA: contentDNA}, nil
}

type preflightResult struct {
	pair        GenerationPair
	contentDNA  dna.Payload
	replayed    bool
	rateLimited bool
}

type quotaWindow struct{ invoked, live int }

func quotaCounts(ctx context.Context, tx *sql.Tx, workspaceID string, cutoff time.Time) (quotaWindow, error) {
	var counts quotaWindow
	err := tx.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE invoked_at >= $2),
			count(*) FILTER (WHERE invoked_at IS NULL AND released_at IS NULL AND reserved_at >= $2)
		FROM workspace_generation_quota_reservations WHERE workspace_id = $1::uuid`, workspaceID, cutoff).Scan(&counts.invoked, &counts.live)
	return counts, err
}

func validateGenerationInput(input GenerationInput) (GenerationInput, error) {
	workspaceID, err := parseUUID(input.WorkspaceID)
	if err != nil {
		return GenerationInput{}, ErrValidation
	}
	dnaVersionID, err := parseUUID(input.BaseContentDNAVersionID)
	if err != nil {
		return GenerationInput{}, ErrValidation
	}
	idempotencyKey, err := parseUUID(input.IdempotencyKey)
	if err != nil {
		return GenerationInput{}, ErrValidation
	}
	if input.RequestedLanguage != "en" && input.RequestedLanguage != "fa" {
		return GenerationInput{}, ErrValidation
	}
	input.WorkspaceID, input.BaseContentDNAVersionID, input.IdempotencyKey = workspaceID, dnaVersionID, idempotencyKey
	return input, nil
}

func fingerprintGeneration(input GenerationInput) string {
	fields := [][2]string{{"generationKind", ai.IdeaGenerationKind}, {"baseContentDnaVersionId", input.BaseContentDNAVersionID}, {"requestedLanguage", input.RequestedLanguage}, {"requestedCount", "20"}}
	var serialized strings.Builder
	for index, field := range fields {
		if index > 0 {
			serialized.WriteByte('|')
		}
		serialized.WriteString(fmt.Sprintf("%d:%s:%d:%s", utf16Length(field[0]), field[0], utf16Length(field[1]), field[1]))
	}
	sum := sha256.Sum256([]byte(serialized.String()))
	return hex.EncodeToString(sum[:])
}

func loadCurrentDNASnapshot(ctx context.Context, tx *sql.Tx, workspaceID, versionID, language string) (dna.Payload, error) {
	var currentID string
	var raw []byte
	err := tx.QueryRowContext(ctx, `
		SELECT v.id::text, v.payload FROM content_dna c
		JOIN content_dna_versions v ON v.content_dna_id = c.id AND v.id = c.current_version_id
		WHERE c.workspace_id = $1::uuid`, workspaceID).Scan(&currentID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return dna.Payload{}, ErrValidation
	}
	if err != nil {
		return dna.Payload{}, errors.New("could not load current Content DNA for idea generation")
	}
	if currentID != versionID {
		return dna.Payload{}, ErrConflict
	}
	payload, err := dna.ParsePayload(raw)
	if err != nil || payload.Readiness() != dna.AIReady {
		return dna.Payload{}, ErrValidation
	}
	if payload.Language == nil || !contains(payload.Language.ContentLanguages, language) {
		return dna.Payload{}, ErrValidation
	}
	return payload, nil
}

func createGenerationPair(ctx context.Context, tx *sql.Tx, input GenerationInput, fingerprint string, now time.Time) (GenerationPair, error) {
	runID, batchID := uuid.NewString(), uuid.NewString()
	settings, _ := json.Marshal(ideaGenerationSettings())
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ai_runs (id, workspace_id, kind, provider, model, prompt_version, generation_settings, status, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, 'PENDING', $8)`,
		runID, input.WorkspaceID, ai.IdeaGenerationKind, ai.IdeaGenerationProvider, ai.IdeaGenerationModel, ai.IdeaGenerationPrompt, settings, now)
	if err != nil {
		return GenerationPair{}, errors.New("could not create AI run")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO idea_generation_batches (id, workspace_id, content_dna_version_id, ai_run_id, idempotency_key, request_fingerprint, requested_language, requested_count, status, created_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, 20, 'PENDING', $8)`,
		batchID, input.WorkspaceID, input.BaseContentDNAVersionID, runID, input.IdempotencyKey, fingerprint, input.RequestedLanguage, now)
	if err != nil {
		return GenerationPair{}, errors.New("could not create idea generation batch")
	}
	pair, err := loadPair(ctx, tx, input.WorkspaceID, batchID, false)
	if err != nil {
		return GenerationPair{}, err
	}
	return pair, nil
}

func ideaGenerationSettings() ai.Settings {
	settings := ai.Settings{}
	settings.StructuredOutput.SchemaName = ai.IdeaGenerationSchema
	settings.StructuredOutput.SchemaVersion = 1
	settings.ReasoningEffort = "medium"
	settings.MaxOutputTokens = ai.IdeaGenerationOutputTokens
	settings.TimeoutSeconds = ai.IdeaGenerationTimeoutSecs
	settings.RetryPolicy.MaxRetries = 0
	settings.ServiceTier = "default"
	return settings
}

func loadPairByIdempotency(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workspaceID, key string) (*GenerationPair, error) {
	var batchID string
	err := queryer.QueryRowContext(ctx, `SELECT id::text FROM idea_generation_batches WHERE workspace_id = $1::uuid AND idempotency_key = $2::uuid`, workspaceID, key).Scan(&batchID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not load idempotent idea generation")
	}
	pair, err := loadPair(ctx, queryer, workspaceID, batchID, false)
	if err != nil {
		return nil, err
	}
	return &pair, nil
}

func loadPair(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workspaceID, batchID string, lock bool) (GenerationPair, error) {
	query := `SELECT b.id::text, b.workspace_id::text, b.content_dna_version_id::text, b.ai_run_id::text, b.idempotency_key::text, b.request_fingerprint, b.requested_language, b.requested_count, b.status, b.error_category, b.created_at, b.started_at, b.completed_at, b.failed_at, r.id::text, r.status, r.provider_request_correlation FROM idea_generation_batches b JOIN ai_runs r ON r.workspace_id = b.workspace_id AND r.id = b.ai_run_id WHERE b.workspace_id = $1::uuid AND b.id = $2::uuid`
	if lock {
		query += ` FOR UPDATE OF b, r`
	}
	var pair GenerationPair
	var errorCategory, correlation sql.NullString
	var started, completed, failed sql.NullTime
	err := queryer.QueryRowContext(ctx, query, workspaceID, batchID).Scan(
		&pair.Batch.ID, &pair.Batch.WorkspaceID, &pair.Batch.ContentDNAVersionID, &pair.Batch.AIRunID,
		&pair.Batch.IdempotencyKey, &pair.Batch.RequestFingerprint, &pair.Batch.RequestedLanguage,
		&pair.Batch.RequestedCount, &pair.Batch.Status, &errorCategory, &pair.Batch.CreatedAt,
		&started, &completed, &failed, &pair.Run.ID, &pair.Run.Status, &correlation,
	)
	if err != nil {
		return GenerationPair{}, err
	}
	if errorCategory.Valid {
		pair.Batch.ErrorCategory = &errorCategory.String
	}
	if started.Valid {
		pair.Batch.StartedAt = &started.Time
	}
	if completed.Valid {
		pair.Batch.CompletedAt = &completed.Time
	}
	if failed.Valid {
		pair.Batch.FailedAt = &failed.Time
	}
	if correlation.Valid {
		pair.Run.ProviderRequestCorrelation = &correlation.String
	}
	return pair, nil
}

func (s *Service) start(ctx context.Context, workspaceID, batchID string) (startResult, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return startResult{}, errors.New("could not begin idea invocation")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, workspaceID); err != nil {
		return startResult{}, errors.New("could not serialize idea invocation")
	}
	pair, err := loadPair(ctx, tx, workspaceID, batchID, true)
	if err != nil {
		return startResult{}, ErrNotFound
	}
	if pair.Batch.Status != "PENDING" || pair.Run.Status != "PENDING" {
		if err := tx.Commit(); err != nil {
			return startResult{}, errors.New("could not finish idea invocation check")
		}
		return startResult{pair: pair}, nil
	}
	var reservationID string
	var invokedAt, releasedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT id::text, invoked_at, released_at FROM workspace_generation_quota_reservations WHERE workspace_id = $1::uuid AND batch_id = $2::uuid FOR UPDATE`, workspaceID, batchID).Scan(&reservationID, &invokedAt, &releasedAt)
	if err != nil || invokedAt.Valid || releasedAt.Valid {
		failed, failErr := failPair(ctx, tx, pair, ai.FailureInterrupted, s.clock.Now().UTC(), "")
		if failErr != nil {
			return startResult{}, failErr
		}
		if err := tx.Commit(); err != nil {
			return startResult{}, errors.New("could not commit interrupted idea invocation")
		}
		return startResult{pair: failed}, nil
	}
	now := s.clock.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE ai_runs SET status = 'RUNNING', started_at = $3 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'PENDING'`, workspaceID, pair.Run.ID, now); err != nil {
		return startResult{}, errors.New("could not start AI run")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE idea_generation_batches SET status = 'RUNNING', started_at = $3 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'PENDING'`, workspaceID, batchID, now); err != nil {
		return startResult{}, errors.New("could not start idea generation batch")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_generation_quota_reservations SET invoked_at = $3 WHERE id = $1::uuid AND workspace_id = $2::uuid AND invoked_at IS NULL AND released_at IS NULL`, reservationID, workspaceID, now); err != nil {
		return startResult{}, errors.New("could not consume workspace quota reservation")
	}
	if err := tx.Commit(); err != nil {
		return startResult{}, errors.New("could not commit idea invocation")
	}
	startedPair, err := loadPair(ctx, s.database, workspaceID, batchID, false)
	if err != nil {
		return startResult{}, errors.New("started idea generation state could not be read")
	}
	return startResult{pair: startedPair, started: true}, nil
}

type startResult struct {
	pair    GenerationPair
	started bool
}

func (s *Service) complete(ctx context.Context, workspaceID, batchID string, result ai.Result) (GenerationPair, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return GenerationPair{}, errors.New("could not begin idea completion")
	}
	defer tx.Rollback()
	pair, err := loadPair(ctx, tx, workspaceID, batchID, true)
	if err != nil {
		return GenerationPair{}, ErrNotFound
	}
	if pair.Batch.Status != "RUNNING" || pair.Run.Status != "RUNNING" {
		if err := tx.Commit(); err != nil {
			return GenerationPair{}, errors.New("could not commit idea completion check")
		}
		return pair, nil
	}
	completedAt := s.clock.Now().UTC()
	outputJSON, err := json.Marshal(result.Output)
	if err != nil {
		return GenerationPair{}, errors.New("could not encode canonical idea output")
	}
	var usageJSON any
	if result.Usage != nil {
		usageJSON, err = json.Marshal(result.Usage)
		if err != nil {
			return GenerationPair{}, errors.New("could not encode safe AI usage")
		}
	}
	for index, idea := range result.Output.Ideas {
		_, err = tx.ExecContext(ctx, `INSERT INTO ideas (id, batch_id, position, title, description, category, language, status, status_changed_at, created_at, updated_at) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, 'NEW', $8, $8, $8)`, uuid.NewString(), batchID, index+1, idea.Title, idea.Description, nullableString(idea.Category), pair.Batch.RequestedLanguage, completedAt)
		if err != nil {
			return GenerationPair{}, errors.New("could not persist canonical ideas")
		}
	}
	correlation := nullableText(result.ProviderRequestCorrelation)
	_, err = tx.ExecContext(ctx, `UPDATE ai_runs SET status = 'COMPLETED', output_snapshot = $3::jsonb, usage = $4::jsonb, provider_request_correlation = COALESCE($5, provider_request_correlation), completed_at = $6 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'RUNNING'`, workspaceID, pair.Run.ID, outputJSON, usageJSON, correlation, completedAt)
	if err != nil {
		return GenerationPair{}, errors.New("could not complete AI run")
	}
	_, err = tx.ExecContext(ctx, `UPDATE idea_generation_batches SET status = 'COMPLETED', completed_at = $3 WHERE workspace_id = $1::uuid AND id = $2::uuid AND status = 'RUNNING'`, workspaceID, batchID, completedAt)
	if err != nil {
		return GenerationPair{}, errors.New("could not complete idea batch")
	}
	if err := tx.Commit(); err != nil {
		return GenerationPair{}, errors.New("could not commit idea generation result")
	}
	completed, err := loadPair(ctx, s.database, workspaceID, batchID, false)
	if err != nil {
		return GenerationPair{}, errors.New("completed idea generation state could not be read")
	}
	return completed, nil
}

func (s *Service) fail(ctx context.Context, workspaceID, batchID string, category ai.FailureCategory, correlation string) (GenerationPair, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return GenerationPair{}, errors.New("could not begin idea failure transition")
	}
	defer tx.Rollback()
	pair, err := loadPair(ctx, tx, workspaceID, batchID, true)
	if err != nil {
		return GenerationPair{}, ErrNotFound
	}
	failed, err := failPair(ctx, tx, pair, category, s.clock.Now().UTC(), correlation)
	if err != nil {
		return GenerationPair{}, err
	}
	if err := tx.Commit(); err != nil {
		return GenerationPair{}, errors.New("could not commit idea failure transition")
	}
	return failed, nil
}

func failPair(ctx context.Context, tx *sql.Tx, pair GenerationPair, category ai.FailureCategory, failedAt time.Time, correlation string) (GenerationPair, error) {
	if pair.Batch.Status != pair.Run.Status {
		return GenerationPair{}, errors.New("idea batch and AI run lifecycle is inconsistent")
	}
	if pair.Batch.Status != "PENDING" && pair.Batch.Status != "RUNNING" {
		return pair, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_runs SET status = 'FAILED', error_category = $3, provider_request_correlation = COALESCE($4, provider_request_correlation), failed_at = $5 WHERE id = $1::uuid AND status = $2`, pair.Run.ID, pair.Run.Status, string(category), nullableText(correlation), failedAt); err != nil {
		return GenerationPair{}, errors.New("could not fail AI run")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE idea_generation_batches SET status = 'FAILED', error_category = $3, failed_at = $4 WHERE id = $1::uuid AND status = $2`, pair.Batch.ID, pair.Batch.Status, string(category), failedAt); err != nil {
		return GenerationPair{}, errors.New("could not fail idea generation batch")
	}
	if pair.Batch.Status == "PENDING" {
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_generation_quota_reservations SET released_at = $3 WHERE workspace_id = $1::uuid AND batch_id = $2::uuid AND invoked_at IS NULL AND released_at IS NULL`, pair.Batch.WorkspaceID, pair.Batch.ID, failedAt); err != nil {
			return GenerationPair{}, errors.New("could not release uninvoked quota reservation")
		}
	}
	return loadPair(ctx, tx, pair.Batch.WorkspaceID, pair.Batch.ID, false)
}

func recoverStaleInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, now time.Time) (int, error) {
	cutoff := now.Add(-staleAttemptAge)
	rows, err := tx.QueryContext(ctx, `SELECT b.id::text FROM idea_generation_batches b JOIN ai_runs r ON r.workspace_id = b.workspace_id AND r.id = b.ai_run_id WHERE b.workspace_id = $1::uuid AND ((b.status = 'PENDING' AND r.status = 'PENDING' AND b.created_at <= $2) OR (b.status = 'RUNNING' AND r.status = 'RUNNING' AND b.started_at <= $2)) FOR UPDATE OF b, r`, workspaceID, cutoff)
	if err != nil {
		return 0, errors.New("could not find stale idea generations")
	}
	var batchIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, errors.New("could not read stale idea generations")
		}
		batchIDs = append(batchIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, errors.New("could not read stale idea generations")
	}
	if err := rows.Close(); err != nil {
		return 0, errors.New("could not close stale idea generation results")
	}
	recovered := 0
	for _, batchID := range batchIDs {
		pair, err := loadPair(ctx, tx, workspaceID, batchID, true)
		if err != nil {
			return 0, errors.New("stale idea generation pair disappeared")
		}
		failed, err := failPair(ctx, tx, pair, ai.FailureInterrupted, now, "")
		if err != nil {
			return 0, err
		}
		if failed.Batch.Status == "FAILED" {
			recovered++
		}
	}
	return recovered, nil
}

func mapProviderFailure(category ai.FailureCategory) error {
	switch category {
	case ai.FailureInvalidOutput:
		return ErrInvalidOutput
	case ai.FailureRateLimited:
		return ErrRateLimited
	default:
		return ErrProvider
	}
}

func parseUUID(value string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil {
		return "", ErrValidation
	}
	return parsed.String(), nil
}

func mustWorkspaceID(value string) workspace.WorkspaceID {
	parsed, _ := workspace.ParseID(value)
	return parsed
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func mustMarshalOutput(output *ai.Output) []byte {
	if output == nil {
		return nil
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil
	}
	return encoded
}

func (s *Service) logGeneration(level slog.Level, message string, userID, workspaceID, batchID, runID, errorCode string) {
	attributes := []any{"user_id", userID, "workspace_id", workspaceID, "module", "ideas", "operation", "generateIdeas"}
	if batchID != "" {
		attributes = append(attributes, "entity_id", batchID)
	}
	if runID != "" {
		attributes = append(attributes, "ai_run_id", runID)
	}
	if errorCode != "" {
		attributes = append(attributes, "error_code", errorCode)
	}
	s.logger.Log(context.Background(), level, message, attributes...)
}
