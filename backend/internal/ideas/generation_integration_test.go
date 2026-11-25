package ideas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/platform/postgres"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

type integrationSubject string

func (s integrationSubject) AuthenticatedUserID() (string, bool) { return string(s), s != "" }

type fixedIdeaProvider struct {
	result ai.Result
	calls  atomic.Int32
}

type mutableIdeaClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableIdeaClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableIdeaClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func (p *fixedIdeaProvider) GenerateIdeas(context.Context, ai.Request) ai.Result {
	p.calls.Add(1)
	return p.result
}

func TestGenerationPersistsTwentyIdeasAndReplaysIdempotently(t *testing.T) {
	database := openIdeaTestDatabase(t)
	subject, workspaceID, dnaVersionID := createIdeaGenerationWorkspace(t, database)
	provider := &fixedIdeaProvider{result: successfulIdeaResult()}
	service := NewService(database, func(string) (ai.GenerateIdeasProvider, error) { return provider, nil }, nil, nil)
	input := GenerationInput{
		WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID,
		RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
	}

	first, err := service.Generate(context.Background(), subject, input)
	if err != nil {
		t.Fatalf("generate ideas: %v", err)
	}
	if first.Batch.Status != "COMPLETED" || first.Replayed {
		t.Fatalf("first generation = %+v", first)
	}
	var ideaCount int
	if err := database.QueryRowContext(context.Background(), `SELECT count(*) FROM ideas WHERE batch_id = $1::uuid`, first.Batch.ID).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != ai.IdeaGenerationCount {
		t.Fatalf("persisted idea count = %d, want %d", ideaCount, ai.IdeaGenerationCount)
	}
	var runStatus, providerName, model, promptVersion string
	var correlation sql.NullString
	var outputSnapshot, safeUsage []byte
	if err := database.QueryRowContext(context.Background(), `SELECT status, provider, model, prompt_version, output_snapshot, usage, provider_request_correlation FROM ai_runs WHERE id = $1::uuid AND workspace_id = $2::uuid`, first.Batch.AIRunID, workspaceID).Scan(&runStatus, &providerName, &model, &promptVersion, &outputSnapshot, &safeUsage, &correlation); err != nil {
		t.Fatal(err)
	}
	var usageFields map[string]json.RawMessage
	var inputTokenCount int
	usageErr := json.Unmarshal(safeUsage, &usageFields)
	if usageErr == nil {
		usageErr = json.Unmarshal(usageFields["inputTokens"], &inputTokenCount)
	}
	if runStatus != "COMPLETED" || providerName != ai.IdeaGenerationProvider || model != ai.IdeaGenerationModel || promptVersion != ai.IdeaGenerationPrompt || !correlation.Valid || correlation.String != "request-test-001" || !strings.Contains(string(outputSnapshot), "\"ideas\"") || usageErr != nil || inputTokenCount != 100 || strings.Contains(string(safeUsage), "raw") {
		t.Fatalf("AI Run lineage = status:%s provider:%s model:%s prompt:%s correlation:%+v snapshot:%s usage:%s", runStatus, providerName, model, promptVersion, correlation, outputSnapshot, safeUsage)
	}
	var invoked, released int
	if err := database.QueryRowContext(context.Background(), `SELECT count(*) FILTER (WHERE invoked_at IS NOT NULL), count(*) FILTER (WHERE released_at IS NOT NULL) FROM workspace_generation_quota_reservations WHERE batch_id = $1::uuid`, first.Batch.ID).Scan(&invoked, &released); err != nil {
		t.Fatal(err)
	}
	if invoked != 1 || released != 0 {
		t.Fatalf("quota reservation lineage invoked=%d released=%d", invoked, released)
	}
	history, err := service.ListBatchHistory(context.Background(), subject, workspaceID)
	if err != nil || len(history.Batches) != 1 || history.SelectedBatchID == nil || *history.SelectedBatchID != first.Batch.ID || history.Batches[0].IdeaCount != ai.IdeaGenerationCount {
		t.Fatalf("batch history = %+v, err=%v", history, err)
	}
	detail, err := service.GetBatchDetail(context.Background(), subject, workspaceID, first.Batch.ID)
	if err != nil || len(detail.Ideas) != ai.IdeaGenerationCount || detail.CanRetry {
		t.Fatalf("batch detail = ideas:%d retry:%v err:%v", len(detail.Ideas), detail.CanRetry, err)
	}
	library, err := service.GetIdeaLibrary(context.Background(), subject, workspaceID, "ALL", nil)
	if err != nil || len(library.Ideas) != ai.IdeaGenerationCount || library.Ideas[0].ContentCount != 0 {
		t.Fatalf("Idea Library = ideas:%d err:%v", len(library.Ideas), err)
	}
	decision, err := service.UpdateDecision(context.Background(), subject, DecisionInput{WorkspaceID: workspaceID, IdeaID: detail.Ideas[0].ID, NextState: "REJECTED", RejectionReason: stringPtr("  Not a fit  ")})
	if err != nil || decision.Status != "REJECTED" || decision.RejectionReason == nil || *decision.RejectionReason != "Not a fit" || decision.IsNoop {
		t.Fatalf("rejection decision = %+v, err=%v", decision, err)
	}
	noop, err := service.UpdateDecision(context.Background(), subject, DecisionInput{WorkspaceID: workspaceID, IdeaID: detail.Ideas[0].ID, NextState: "REJECTED", RejectionReason: stringPtr("Not a fit")})
	if err != nil || !noop.IsNoop {
		t.Fatalf("same-state decision = %+v, err=%v", noop, err)
	}
	accepted, err := service.UpdateDecision(context.Background(), subject, DecisionInput{WorkspaceID: workspaceID, IdeaID: detail.Ideas[0].ID, NextState: "ACCEPTED", RejectionReason: stringPtr("discard this")})
	if err != nil || accepted.Status != "ACCEPTED" || accepted.RejectionReason != nil {
		t.Fatalf("acceptance decision = %+v, err=%v", accepted, err)
	}
	queue := content.NewQueueService(database)
	queueItems, err := queue.Get(context.Background(), subject, workspaceID)
	if err != nil || len(queueItems) != 1 || queueItems[0].ID != accepted.ID || queueItems[0].ProductionQueuePosition != 1 || queueItems[0].LastAttempt != nil {
		t.Fatalf("production queue = %+v, err=%v", queueItems, err)
	}
	queueItems, err = queue.Reorder(context.Background(), subject, workspaceID, []string{accepted.ID})
	if err != nil || len(queueItems) != 1 || queueItems[0].ProductionQueuePosition != 1 {
		t.Fatalf("reordered production queue = %+v, err=%v", queueItems, err)
	}

	replay, err := service.Generate(context.Background(), subject, input)
	if err != nil {
		t.Fatalf("replay generation: %v", err)
	}
	if !replay.Replayed || replay.Batch.ID != first.Batch.ID || provider.calls.Load() != 1 {
		t.Fatalf("replay = %+v, provider calls = %d", replay, provider.calls.Load())
	}

	input.RequestedLanguage = "fa"
	if _, err := service.Generate(context.Background(), subject, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused idempotency key with changed request = %v, want conflict", err)
	}
}

func TestGenerationQuotaAndInvalidOutputFailureAreAtomic(t *testing.T) {
	database := openIdeaTestDatabase(t)
	subject, workspaceID, dnaVersionID := createIdeaGenerationWorkspace(t, database)
	provider := &fixedIdeaProvider{result: successfulIdeaResult()}
	service := NewService(database, func(string) (ai.GenerateIdeasProvider, error) { return provider, nil }, nil, nil)
	for index := 0; index < tenMinuteQuota; index++ {
		_, err := service.Generate(context.Background(), subject, GenerationInput{
			WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID,
			RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
		})
		if err != nil {
			t.Fatalf("generation %d: %v", index+1, err)
		}
	}
	if _, err := service.Generate(context.Background(), subject, GenerationInput{
		WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID,
		RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
	}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("quota result = %v, want rate limit", err)
	}
	if provider.calls.Load() != tenMinuteQuota {
		t.Fatalf("provider calls = %d, want %d", provider.calls.Load(), tenMinuteQuota)
	}

	otherSubject, otherWorkspace, otherDNA := createIdeaGenerationWorkspace(t, database)
	invalid := successfulIdeaResult()
	invalid.Output.Ideas = invalid.Output.Ideas[:1]
	invalidProvider := &fixedIdeaProvider{result: invalid}
	invalidService := NewService(database, func(string) (ai.GenerateIdeasProvider, error) { return invalidProvider, nil }, nil, nil)
	result, err := invalidService.Generate(context.Background(), otherSubject, GenerationInput{
		WorkspaceID: otherWorkspace, BaseContentDNAVersionID: otherDNA,
		RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
	})
	if !errors.Is(err, ErrInvalidOutput) || result.Batch.Status != "FAILED" || result.Batch.ErrorCategory == nil || *result.Batch.ErrorCategory != string(ai.FailureInvalidOutput) {
		t.Fatalf("invalid output transition = %+v, %v", result, err)
	}
	var ideasCount, completedRuns int
	if err := database.QueryRowContext(context.Background(), `SELECT count(*) FROM ideas WHERE batch_id = $1::uuid`, result.Batch.ID).Scan(&ideasCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(context.Background(), `SELECT count(*) FROM ai_runs WHERE id = $1::uuid AND status = 'COMPLETED'`, result.Batch.AIRunID).Scan(&completedRuns); err != nil {
		t.Fatal(err)
	}
	if ideasCount != 0 || completedRuns != 0 {
		t.Fatalf("invalid output persisted ideas=%d completed runs=%d", ideasCount, completedRuns)
	}
	invalidProvider.result = successfulIdeaResult()
	retry, err := invalidService.RetryBatch(context.Background(), otherSubject, otherWorkspace, result.Batch.ID)
	if err != nil || retry.Batch.Status != "COMPLETED" || retry.Batch.ID == result.Batch.ID || invalidProvider.calls.Load() != 2 {
		t.Fatalf("failed-batch retry = %+v, calls=%d, err=%v", retry, invalidProvider.calls.Load(), err)
	}
}

func TestGenerationEnforcesDailyQuotaAndReopensAfterWindow(t *testing.T) {
	database := openIdeaTestDatabase(t)
	subject, workspaceID, dnaVersionID := createIdeaGenerationWorkspace(t, database)
	provider := &fixedIdeaProvider{result: successfulIdeaResult()}
	mutableClock := &mutableIdeaClock{now: time.Now().UTC().Truncate(time.Microsecond)}
	service := NewService(database, func(string) (ai.GenerateIdeasProvider, error) { return provider, nil }, mutableClock, nil)
	for index := 0; index < twentyFourQuota; index++ {
		if index > 0 && index%tenMinuteQuota == 0 {
			mutableClock.Advance(11 * time.Minute)
		}
		_, err := service.Generate(context.Background(), subject, GenerationInput{
			WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID, RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
		})
		if err != nil {
			t.Fatalf("daily quota generation %d: %v", index+1, err)
		}
	}
	if _, err := service.Generate(context.Background(), subject, GenerationInput{
		WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID, RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
	}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("13th generation in daily window = %v, want rate limit", err)
	}
	if provider.calls.Load() != twentyFourQuota {
		t.Fatalf("daily quota provider calls = %d, want %d", provider.calls.Load(), twentyFourQuota)
	}
	mutableClock.Advance(24*time.Hour + time.Second)
	if _, err := service.Generate(context.Background(), subject, GenerationInput{
		WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID, RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
	}); err != nil {
		t.Fatalf("generation after daily window expired: %v", err)
	}
}

func TestBatchHistoryRecoversStalePendingAndRunningAttempts(t *testing.T) {
	database := openIdeaTestDatabase(t)
	subject, workspaceID, dnaVersionID := createIdeaGenerationWorkspace(t, database)
	mutableClock := &mutableIdeaClock{now: time.Now().UTC().Truncate(time.Microsecond)}
	service := NewService(database, nil, mutableClock, nil)
	ctx := context.Background()
	createReserved := func() GenerationPair {
		input, err := validateGenerationInput(GenerationInput{WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID, RequestedLanguage: "en", IdempotencyKey: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		preflight, err := service.reserve(ctx, subject, input, fingerprintGeneration(input))
		if err != nil {
			t.Fatal(err)
		}
		return preflight.pair
	}

	pending := createReserved()
	mutableClock.Advance(3 * time.Minute)
	if _, err := service.ListBatchHistory(ctx, subject, workspaceID); err != nil {
		t.Fatalf("recover stale pending attempt: %v", err)
	}
	assertStaleAttemptState(t, database, pending, false)

	running := createReserved()
	started, err := service.start(ctx, workspaceID, running.Batch.ID)
	if err != nil || !started.started {
		t.Fatalf("start attempt = %+v, err=%v", started, err)
	}
	mutableClock.Advance(3 * time.Minute)
	if _, err := service.ListBatchHistory(ctx, subject, workspaceID); err != nil {
		t.Fatalf("recover stale running attempt: %v", err)
	}
	assertStaleAttemptState(t, database, running, true)
}

func assertStaleAttemptState(t *testing.T, database *sql.DB, pair GenerationPair, wasInvoked bool) {
	t.Helper()
	var batchStatus, runStatus, category string
	if err := database.QueryRowContext(context.Background(), `SELECT b.status, r.status, b.error_category FROM idea_generation_batches b JOIN ai_runs r ON r.id = b.ai_run_id WHERE b.id = $1::uuid`, pair.Batch.ID).Scan(&batchStatus, &runStatus, &category); err != nil {
		t.Fatal(err)
	}
	if batchStatus != "FAILED" || runStatus != "FAILED" || category != string(ai.FailureInterrupted) {
		t.Fatalf("recovered lifecycle = %s/%s/%s", batchStatus, runStatus, category)
	}
	var invoked, released int
	if err := database.QueryRowContext(context.Background(), `SELECT count(*) FILTER (WHERE invoked_at IS NOT NULL), count(*) FILTER (WHERE released_at IS NOT NULL) FROM workspace_generation_quota_reservations WHERE batch_id = $1::uuid`, pair.Batch.ID).Scan(&invoked, &released); err != nil {
		t.Fatal(err)
	}
	if wasInvoked && (invoked != 1 || released != 0) {
		t.Fatalf("invoked recovery quota = %d/%d", invoked, released)
	}
	if !wasInvoked && (invoked != 0 || released != 1) {
		t.Fatalf("pending recovery quota = %d/%d", invoked, released)
	}
}

func TestProductionQueueSerializesReordersAndRejectsStaleMembership(t *testing.T) {
	database := openIdeaTestDatabase(t)
	subject, workspaceID, dnaVersionID := createIdeaGenerationWorkspace(t, database)
	provider := &fixedIdeaProvider{result: successfulIdeaResult()}
	service := NewService(database, func(string) (ai.GenerateIdeasProvider, error) { return provider, nil }, nil, nil)
	generated, err := service.Generate(context.Background(), subject, GenerationInput{
		WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersionID, RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.GetBatchDetail(context.Background(), subject, workspaceID, generated.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, idea := range detail.Ideas[:2] {
		if _, err := service.UpdateDecision(context.Background(), subject, DecisionInput{WorkspaceID: workspaceID, IdeaID: idea.ID, NextState: "ACCEPTED"}); err != nil {
			t.Fatal(err)
		}
	}
	queue := content.NewQueueService(database)
	items, err := queue.Get(context.Background(), subject, workspaceID)
	if err != nil || len(items) != 2 {
		t.Fatalf("initial queue = %+v, err=%v", items, err)
	}
	if _, err := queue.Reorder(context.Background(), subject, workspaceID, []string{items[0].ID}); !errors.Is(err, content.ErrQueueConflict) {
		t.Fatalf("stale queue membership result = %v, want conflict", err)
	}
	firstOrder := []string{items[1].ID, items[0].ID}
	secondOrder := []string{items[0].ID, items[1].ID}
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	var workers sync.WaitGroup
	for _, order := range [][]string{firstOrder, secondOrder} {
		workers.Add(1)
		go func(order []string) {
			defer workers.Done()
			<-start
			_, reorderErr := queue.Reorder(context.Background(), subject, workspaceID, order)
			errorsOut <- reorderErr
		}(order)
	}
	close(start)
	workers.Wait()
	close(errorsOut)
	for reorderErr := range errorsOut {
		if reorderErr != nil {
			t.Fatalf("concurrent queue reorder: %v", reorderErr)
		}
	}
	final, err := queue.Get(context.Background(), subject, workspaceID)
	if err != nil || len(final) != 2 || final[0].ProductionQueuePosition != 1 || final[1].ProductionQueuePosition != 2 || final[0].ID == final[1].ID {
		t.Fatalf("queue after concurrent reorder = %+v, err=%v", final, err)
	}
}

func successfulIdeaResult() ai.Result {
	output := &ai.Output{SchemaVersion: 1, Ideas: make([]ai.Idea, ai.IdeaGenerationCount)}
	for index := range output.Ideas {
		output.Ideas[index] = ai.Idea{Title: "Idea " + string(rune('A'+index)), Description: "A useful proposal."}
	}
	inputTokens, outputTokens, totalTokens := int64(100), int64(200), int64(300)
	return ai.Result{Output: output, Usage: &ai.Usage{InputTokens: &inputTokens, OutputTokens: &outputTokens, TotalTokens: &totalTokens}, ProviderRequestCorrelation: "request-test-001"}
}

func stringPtr(value string) *string { return &value }

func openIdeaTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || strings.TrimPrefix(parsed.Path, "/") == "" || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must target a database ending in _test")
	}
	if primary := os.Getenv("DATABASE_URL"); primary != "" {
		p, parseErr := url.Parse(primary)
		if parseErr != nil || strings.EqualFold(parsed.Host+parsed.Path, p.Host+p.Path) {
			t.Fatal("TEST_DATABASE_URL must not identify DATABASE_URL")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	database, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func createIdeaGenerationWorkspace(t *testing.T, database *sql.DB) (integrationSubject, string, string) {
	t.Helper()
	ctx := context.Background()
	userID, workspaceID := uuid.NewString(), uuid.NewString()
	if _, err := database.ExecContext(ctx, `INSERT INTO "user" (id, name, email) VALUES ($1, 'Ideas test', $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("create Ideas user: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO workspaces (id, name) VALUES ($1::uuid, 'Ideas test workspace')`, workspaceID); err != nil {
		t.Fatalf("create Ideas workspace: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1::uuid, $2, 'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("create Ideas owner membership: %v", err)
	}
	cleanupIdeaWorkspace(t, database, userID, workspaceID)
	service := dna.NewService(database)
	payload := []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Creator"},"audience":{"targetAudienceDescription":"Readers"},"expertise":{"primaryTopics":["Education"]},"voice":{"toneTraits":["Clear"]},"goals":{"contentGoals":["Teach"]},"language":{"defaultContentLanguage":"en","contentLanguages":["en","fa"]}}`)
	version, err := service.Save(ctx, integrationSubject(userID), workspaceID, nil, payload)
	if err != nil {
		t.Fatalf("create AI-ready DNA: %v", err)
	}
	if version.Readiness != dna.AIReady {
		t.Fatalf("DNA readiness = %q, want AI_READY", version.Readiness)
	}
	return integrationSubject(userID), workspaceID, version.ID
}

func cleanupIdeaWorkspace(t *testing.T, database *sql.DB, userID, workspaceID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = database.ExecContext(ctx, `DELETE FROM workspace_generation_quota_reservations WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM ideas WHERE batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM idea_generation_batches WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM ai_runs WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM content_dna_versions WHERE content_dna_id IN (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM content_dna WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
}

var _ workspace.MembershipSubject = integrationSubject("")
