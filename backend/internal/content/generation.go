package content

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
	"bettercontent/backend/internal/platform/textnorm"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

const (
	contentGenerationTenMinuteQuota = 2
	contentGenerationDailyQuota     = 8
	contentGenerationTenMinutes     = 10 * time.Minute
	contentGenerationDailyWindow    = 24 * time.Hour
	contentGenerationStaleAge       = 105 * time.Second
)

var (
	ErrGenerationValidation    = errors.New("content generation request is invalid")
	ErrGenerationConflict      = errors.New("content generation state changed")
	ErrGenerationRateLimited   = errors.New("workspace content generation quota was reached")
	ErrGenerationNotFound      = errors.New("content generation resource was not found")
	ErrGenerationInvalidOutput = errors.New("content provider returned invalid output")
	ErrGenerationProvider      = errors.New("content provider operation failed")
)

type ContentProviderFactory func(userID string) (ai.GenerateContentProvider, error)

type Service struct {
	database        *sql.DB
	providerFactory ContentProviderFactory
	clock           clock.Clock
	logger          *slog.Logger
}

func NewService(database *sql.DB, providerFactory ContentProviderFactory, sourceClock clock.Clock, logger *slog.Logger) *Service {
	if sourceClock == nil {
		sourceClock = clock.System{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{database: database, providerFactory: providerFactory, clock: sourceClock, logger: logger}
}

type ContentGenerationInput struct {
	WorkspaceID             string  `json:"workspaceId"`
	SourceIdeaID            string  `json:"sourceIdeaId"`
	BaseContentDNAVersionID string  `json:"baseContentDnaVersionId"`
	RequestedLanguage       string  `json:"requestedLanguage"`
	Format                  string  `json:"format"`
	Instructions            *string `json:"instructions,omitempty"`
	IdempotencyKey          string  `json:"idempotencyKey"`
}

type ContentGenerationAttempt struct {
	ID                  string     `json:"id"`
	WorkspaceID         string     `json:"-"`
	SourceIdeaID        string     `json:"sourceIdeaId"`
	ContentDNAVersionID string     `json:"contentDnaVersionId"`
	AIRunID             string     `json:"aiRunId"`
	RequestedLanguage   string     `json:"requestedLanguage"`
	Format              string     `json:"format"`
	Instructions        *string    `json:"instructions"`
	RequestFingerprint  string     `json:"requestFingerprint"`
	Status              string     `json:"status"`
	ErrorCategory       *string    `json:"errorCategory"`
	RateLimitSource     *string    `json:"rateLimitSource"`
	CreatedAt           time.Time  `json:"createdAt"`
	StartedAt           *time.Time `json:"startedAt"`
	CompletedAt         *time.Time `json:"completedAt"`
	FailedAt            *time.Time `json:"failedAt"`
}

type ContentGenerationResult struct {
	Attempt   ContentGenerationAttempt `json:"attempt"`
	ContentID *string                  `json:"contentId"`
	Replayed  bool                     `json:"replayed"`
}

type generationPair struct {
	attempt        ContentGenerationAttempt
	request        ContentGenerationInput
	fingerprint    string
	runStatus      string
	runID          string
	runCorrelation *string
	contentID      *string
}

type generationPreflight struct {
	pair        generationPair
	replayed    bool
	rateLimited bool
}

func (s *Service) Generate(ctx context.Context, subject workspace.MembershipSubject, input ContentGenerationInput) (ContentGenerationResult, error) {
	userID, authenticated := subject.AuthenticatedUserID()
	if !authenticated || userID == "" {
		return ContentGenerationResult{}, workspace.ErrUnauthenticated
	}
	input, err := validateContentGenerationInput(input)
	if err != nil {
		return ContentGenerationResult{}, err
	}
	workspaceID, _ := workspace.ParseID(input.WorkspaceID)
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return ContentGenerationResult{}, err
	}
	fingerprint := fingerprintContentGeneration(input)
	preflight, err := s.reserveGeneration(ctx, subject, input, fingerprint)
	if err != nil {
		return ContentGenerationResult{}, err
	}
	if preflight.rateLimited {
		return ContentGenerationResult{}, ErrGenerationRateLimited
	}
	if preflight.pair.attempt.Status != "PENDING" {
		return s.currentGenerationResult(ctx, preflight.pair, preflight.replayed), nil
	}
	started, err := s.startGenerationInvocation(ctx, input.WorkspaceID, preflight.pair.attempt.ID, preflight.pair.runID)
	if err != nil {
		return ContentGenerationResult{}, err
	}
	if !started.started {
		return s.currentGenerationResult(ctx, started.pair, true), nil
	}
	accepted, err := s.loadAcceptedGenerationInputs(ctx, started.pair)
	if err != nil {
		failed, failErr := s.failGenerationInvocation(ctx, input.WorkspaceID, started.pair, ai.FailureInterrupted, "")
		if failErr != nil {
			return ContentGenerationResult{}, failErr
		}
		return s.currentGenerationResult(ctx, failed, false), ErrGenerationConflict
	}
	if s.providerFactory == nil {
		failed, err := s.failGenerationInvocation(ctx, input.WorkspaceID, started.pair, ai.FailureUnknown, "")
		if err != nil {
			return ContentGenerationResult{}, err
		}
		return s.currentGenerationResult(ctx, failed, false), ErrGenerationProvider
	}
	provider, err := s.providerFactory(userID)
	if err != nil {
		failed, failErr := s.failGenerationInvocation(ctx, input.WorkspaceID, started.pair, ai.FailureUnknown, "")
		if failErr != nil {
			return ContentGenerationResult{}, failErr
		}
		return s.currentGenerationResult(ctx, failed, false), ErrGenerationProvider
	}
	providerCtx, cancel := context.WithTimeout(ctx, ai.ContentGenerationTimeoutSecs*time.Second)
	providerResult := provider.GenerateContent(providerCtx, ai.ContentRequest{
		GenerationKind:    ai.ContentGenerationKind,
		SourceIdea:        accepted.idea,
		ContentDNA:        accepted.dna,
		RequestedLanguage: input.RequestedLanguage,
		Format:            input.Format,
		Instructions:      input.Instructions,
	})
	contextErr := providerCtx.Err()
	cancel()
	if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(contextErr, context.Canceled) {
		providerResult = ai.ContentResult{ErrorCategory: ai.FailureTimeout}
	}
	if !providerResult.Succeeded() {
		category := providerResult.ErrorCategory
		if category == "" {
			category = ai.FailureUnknown
		}
		failed, failErr := s.failGenerationInvocation(ctx, input.WorkspaceID, started.pair, category, providerResult.ProviderRequestCorrelation)
		if failErr != nil {
			return ContentGenerationResult{}, failErr
		}
		return s.currentGenerationResult(ctx, failed, false), mapContentProviderFailure(category)
	}
	if !providerResult.Output.Valid() {
		failed, failErr := s.failGenerationInvocation(ctx, input.WorkspaceID, started.pair, ai.FailureInvalidOutput, providerResult.ProviderRequestCorrelation)
		if failErr != nil {
			return ContentGenerationResult{}, failErr
		}
		return s.currentGenerationResult(ctx, failed, false), ErrGenerationInvalidOutput
	}
	completed, contentID, err := s.completeGenerationInvocation(ctx, userID, input.WorkspaceID, started.pair, providerResult)
	if err != nil {
		return ContentGenerationResult{}, err
	}
	return ContentGenerationResult{Attempt: completed.attempt, ContentID: contentID, Replayed: false}, nil
}

func validateContentGenerationInput(input ContentGenerationInput) (ContentGenerationInput, error) {
	workspaceID, err := parseContentUUID(input.WorkspaceID)
	if err != nil {
		return ContentGenerationInput{}, ErrGenerationValidation
	}
	sourceIdeaID, err := parseContentUUID(input.SourceIdeaID)
	if err != nil {
		return ContentGenerationInput{}, ErrGenerationValidation
	}
	dnaVersionID, err := parseContentUUID(input.BaseContentDNAVersionID)
	if err != nil {
		return ContentGenerationInput{}, ErrGenerationValidation
	}
	idempotencyKey, err := parseContentUUID(input.IdempotencyKey)
	if err != nil {
		return ContentGenerationInput{}, ErrGenerationValidation
	}
	if (input.RequestedLanguage != "en" && input.RequestedLanguage != "fa") || (input.Format != "SHORT_VIDEO" && input.Format != "LONG_VIDEO") {
		return ContentGenerationInput{}, ErrGenerationValidation
	}
	if input.Instructions != nil {
		canonical := textnorm.TrimECMAScript(*input.Instructions)
		if canonical == "" || utf16Length(canonical) > 1000 {
			return ContentGenerationInput{}, ErrGenerationValidation
		}
		input.Instructions = &canonical
	}
	input.WorkspaceID, input.SourceIdeaID, input.BaseContentDNAVersionID, input.IdempotencyKey = workspaceID, sourceIdeaID, dnaVersionID, idempotencyKey
	return input, nil
}

func fingerprintContentGeneration(input ContentGenerationInput) string {
	instructions := ""
	if input.Instructions != nil {
		instructions = *input.Instructions
	}
	fields := [][2]string{
		{"generationKind", ai.ContentGenerationKind},
		{"sourceIdeaId", input.SourceIdeaID},
		{"baseContentDnaVersionId", input.BaseContentDNAVersionID},
		{"requestedLanguage", input.RequestedLanguage},
		{"format", input.Format},
		{"instructions", instructions},
	}
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

func parseContentUUID(value string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil {
		return "", ErrGenerationValidation
	}
	return parsed.String(), nil
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func mapContentProviderFailure(category ai.FailureCategory) error {
	switch category {
	case ai.FailureInvalidOutput:
		return ErrGenerationInvalidOutput
	case ai.FailureRateLimited:
		return ErrGenerationRateLimited
	default:
		return ErrGenerationProvider
	}
}

func (s *Service) reserveGeneration(ctx context.Context, subject workspace.MembershipSubject, input ContentGenerationInput, fingerprint string) (generationPreflight, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return generationPreflight{}, errors.New("could not begin Content generation reservation")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, input.WorkspaceID); err != nil {
		return generationPreflight{}, errors.New("could not serialize Content generation")
	}
	workspaceID, _ := workspace.ParseID(input.WorkspaceID)
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceID); err != nil {
		return generationPreflight{}, err
	}
	if err := lockWorkspace(ctx, tx, input.WorkspaceID); err != nil {
		return generationPreflight{}, err
	}
	if existing, err := loadGenerationPair(ctx, tx, input.WorkspaceID, input.IdempotencyKey, true); err != nil {
		return generationPreflight{}, err
	} else if existing != nil {
		if existing.fingerprint != fingerprint {
			return generationPreflight{}, ErrGenerationConflict
		}
		if err := tx.Commit(); err != nil {
			return generationPreflight{}, errors.New("could not complete Content idempotency lookup")
		}
		return generationPreflight{pair: *existing, replayed: true}, nil
	}
	if err := recoverStaleContentGenerations(ctx, tx, input.WorkspaceID, s.clock.Now().UTC()); err != nil {
		return generationPreflight{}, err
	}
	if existing, err := loadGenerationPair(ctx, tx, input.WorkspaceID, input.IdempotencyKey, true); err != nil {
		return generationPreflight{}, err
	} else if existing != nil {
		if existing.fingerprint != fingerprint {
			return generationPreflight{}, ErrGenerationConflict
		}
		if err := tx.Commit(); err != nil {
			return generationPreflight{}, errors.New("could not complete recovered Content idempotency lookup")
		}
		return generationPreflight{pair: *existing, replayed: true}, nil
	}
	_, err = loadGenerationPreflightInputs(ctx, tx, input)
	if err != nil {
		return generationPreflight{}, err
	}
	now := s.clock.Now().UTC()
	tenMinute, err := contentGenerationQuotaCounts(ctx, tx, input.WorkspaceID, now.Add(-contentGenerationTenMinutes))
	if err != nil {
		return generationPreflight{}, errors.New("could not check Content generation quota")
	}
	day, err := contentGenerationQuotaCounts(ctx, tx, input.WorkspaceID, now.Add(-contentGenerationDailyWindow))
	if err != nil {
		return generationPreflight{}, errors.New("could not check daily Content generation quota")
	}
	if tenMinute.invoked+tenMinute.live >= contentGenerationTenMinuteQuota || day.invoked+day.live >= contentGenerationDailyQuota {
		if err := tx.Commit(); err != nil {
			return generationPreflight{}, errors.New("could not complete Content quota check")
		}
		return generationPreflight{rateLimited: true}, nil
	}
	pair, err := createContentGenerationPair(ctx, tx, input, fingerprint, now)
	if err != nil {
		return generationPreflight{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_content_generation_quota_reservations (workspace_id, attempt_id, reserved_at) VALUES ($1::uuid, $2::uuid, $3)`, input.WorkspaceID, pair.attempt.ID, now); err != nil {
		return generationPreflight{}, errors.New("could not reserve Content generation quota")
	}
	if err := tx.Commit(); err != nil {
		return generationPreflight{}, errors.New("could not commit Content generation reservation")
	}
	return generationPreflight{pair: pair}, nil
}

type contentQuotaWindow struct{ invoked, live int }

func contentGenerationQuotaCounts(ctx context.Context, tx *sql.Tx, workspaceID string, cutoff time.Time) (contentQuotaWindow, error) {
	var counts contentQuotaWindow
	err := tx.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE invoked_at >= $2),
		       count(*) FILTER (WHERE invoked_at IS NULL AND released_at IS NULL AND reserved_at >= $2)
		FROM workspace_content_generation_quota_reservations WHERE workspace_id = $1::uuid`, workspaceID, cutoff).Scan(&counts.invoked, &counts.live)
	return counts, err
}

func loadGenerationPreflightInputs(ctx context.Context, tx *sql.Tx, input ContentGenerationInput) (dna.Payload, error) {
	var ideaID string
	err := tx.QueryRowContext(ctx, `
		SELECT i.id::text FROM ideas i
		JOIN idea_generation_batches b ON b.id = i.batch_id
		WHERE b.workspace_id = $1::uuid AND i.id = $2::uuid AND i.status = 'ACCEPTED'
		AND NOT EXISTS (SELECT 1 FROM contents c WHERE c.workspace_id = b.workspace_id AND c.source_idea_id = i.id)
		FOR UPDATE OF i`, input.WorkspaceID, input.SourceIdeaID).Scan(&ideaID)
	if errors.Is(err, sql.ErrNoRows) {
		return dna.Payload{}, ErrGenerationValidation
	}
	if err != nil {
		return dna.Payload{}, errors.New("could not validate source Idea for Content generation")
	}
	var currentVersionID string
	var payloadRaw []byte
	err = tx.QueryRowContext(ctx, `
		SELECT current.id::text, current.payload FROM content_dna d
		JOIN content_dna_versions current ON current.content_dna_id = d.id AND current.id = d.current_version_id
		WHERE d.workspace_id = $1::uuid`, input.WorkspaceID).Scan(&currentVersionID, &payloadRaw)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && currentVersionID != input.BaseContentDNAVersionID) {
		return dna.Payload{}, ErrGenerationConflict
	}
	if err != nil {
		return dna.Payload{}, errors.New("could not load current Content DNA for generation")
	}
	payload, err := dna.ParsePayload(payloadRaw)
	if err != nil || payload.Readiness() != dna.AIReady {
		return dna.Payload{}, ErrGenerationValidation
	}
	if payload.Language == nil || !containsContentLanguage(payload.Language.ContentLanguages, input.RequestedLanguage) {
		return dna.Payload{}, ErrGenerationValidation
	}
	return payload, nil
}

func containsContentLanguage(languages []string, requested string) bool {
	for _, language := range languages {
		if language == requested {
			return true
		}
	}
	return false
}

func createContentGenerationPair(ctx context.Context, tx *sql.Tx, input ContentGenerationInput, fingerprint string, now time.Time) (generationPair, error) {
	attemptID, runID := uuid.NewString(), uuid.NewString()
	settings := ai.Settings{}
	settings.StructuredOutput.SchemaName = ai.ContentGenerationSchema
	settings.StructuredOutput.SchemaVersion = 1
	settings.ReasoningEffort = "medium"
	settings.MaxOutputTokens = ai.ContentGenerationOutputTokens
	settings.TimeoutSeconds = ai.ContentGenerationTimeoutSecs
	settings.RetryPolicy.MaxRetries = 0
	settings.ServiceTier = "default"
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return generationPair{}, errors.New("could not encode Content generation settings")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_runs (id, workspace_id, kind, provider, model, prompt_version, generation_settings, status, created_at) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, 'PENDING', $8)`, runID, input.WorkspaceID, ai.ContentGenerationKind, ai.IdeaGenerationProvider, ai.IdeaGenerationModel, ai.ContentGenerationPrompt, settingsJSON, now); err != nil {
		return generationPair{}, errors.New("could not create Content AI Run")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO content_generation_attempts (id, workspace_id, source_idea_id, content_dna_version_id, requested_language, format, instructions, idempotency_key, request_fingerprint, ai_run_id, status, created_at) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9, $10::uuid, 'PENDING', $11)`, attemptID, input.WorkspaceID, input.SourceIdeaID, input.BaseContentDNAVersionID, input.RequestedLanguage, input.Format, nullableGenerationInstructions(input.Instructions), input.IdempotencyKey, fingerprint, runID, now); err != nil {
		return generationPair{}, errors.New("could not create Content generation attempt")
	}
	return loadGenerationPairByAttempt(ctx, tx, input.WorkspaceID, attemptID, false)
}

func nullableGenerationInstructions(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

type acceptedGenerationInputs struct {
	idea ai.SourceIdea
	dna  dna.Payload
}
