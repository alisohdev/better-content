package content_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/ideas"
	"bettercontent/backend/internal/platform/postgres"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

type contentTestSubject string

func (s contentTestSubject) AuthenticatedUserID() (string, bool) { return string(s), s != "" }

type fixedIdeaProvider struct{ output *ai.Output }

func (p fixedIdeaProvider) GenerateIdeas(context.Context, ai.Request) ai.Result {
	return ai.Result{Output: p.output}
}

type fixedContentProvider struct {
	output *document.V4
	calls  atomic.Int32
}

type gatedContentProvider struct {
	output  *document.V4
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *gatedContentProvider) GenerateContent(context.Context, ai.ContentRequest) ai.ContentResult {
	p.calls.Add(1)
	close(p.entered)
	<-p.release
	return ai.ContentResult{Output: p.output}
}

func (p *fixedContentProvider) GenerateContent(context.Context, ai.ContentRequest) ai.ContentResult {
	p.calls.Add(1)
	return ai.ContentResult{Output: p.output}
}

func TestGenerationPersistsCanonicalLineageAndReplaysWithoutSecondProviderCall(t *testing.T) {
	db := openContentDatabase(t)
	subject, workspaceID, sourceIdeaID, dnaVersionID := createAcceptedContentIdea(t, db)
	providerDocument, err := document.ParseGenerated([]byte(`{"schemaVersion":1,"script":{"blocks":[{"type":"paragraph","text":"  A useful script.  ","performanceDirections":[],"editDirections":[]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	provider := &fixedContentProvider{output: &providerDocument}
	service := content.NewService(db, func(string) (ai.GenerateContentProvider, error) { return provider, nil }, nil, nil)
	input := content.ContentGenerationInput{
		WorkspaceID: workspaceID, SourceIdeaID: sourceIdeaID, BaseContentDNAVersionID: dnaVersionID,
		RequestedLanguage: "en", Format: "SHORT_VIDEO", IdempotencyKey: uuid.NewString(),
	}

	first, err := service.Generate(context.Background(), subject, input)
	if err != nil {
		t.Fatalf("generate Content: %v", err)
	}
	if first.Attempt.Status != "COMPLETED" || first.Replayed || first.ContentID == nil {
		t.Fatalf("generation result = %+v", first)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one", provider.calls.Load())
	}
	var draftJSON, versionJSON, runJSON []byte
	if err := db.QueryRowContext(context.Background(), `SELECT d.document, v.document, r.output_snapshot FROM contents c JOIN content_drafts d ON d.content_id = c.id JOIN content_versions v ON v.content_id = c.id AND v.version_number = 1 JOIN ai_runs r ON r.id = v.ai_run_id WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid`, *first.ContentID, workspaceID).Scan(&draftJSON, &versionJSON, &runJSON); err != nil {
		t.Fatal(err)
	}
	var draftValue, versionValue, runValue any
	for name, pair := range map[string][]byte{"draft": draftJSON, "version": versionJSON, "run": runJSON} {
		var value any
		if err := json.Unmarshal(pair, &value); err != nil {
			t.Fatalf("decode %s JSON: %v", name, err)
		}
		switch name {
		case "draft":
			draftValue = value
		case "version":
			versionValue = value
		case "run":
			runValue = value
		}
	}
	if !equalJSON(draftValue, versionValue) || !equalJSON(versionValue, runValue) {
		t.Fatalf("initial canonical results differ: draft=%s version=%s run=%s", draftJSON, versionJSON, runJSON)
	}
	var runStatus, versionSource string
	var versionNumber, revision int
	if err := db.QueryRowContext(context.Background(), `SELECT r.status, v.source, v.version_number, d.revision FROM contents c JOIN content_versions v ON v.content_id = c.id JOIN ai_runs r ON r.id = v.ai_run_id JOIN content_drafts d ON d.content_id = c.id WHERE c.id = $1::uuid`, *first.ContentID).Scan(&runStatus, &versionSource, &versionNumber, &revision); err != nil {
		t.Fatal(err)
	}
	if runStatus != "COMPLETED" || versionSource != "AI_GENERATED" || versionNumber != 1 || revision != 1 {
		t.Fatalf("persisted initial lineage = run:%s source:%s version:%d revision:%d", runStatus, versionSource, versionNumber, revision)
	}
	var invoked, released int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FILTER (WHERE invoked_at IS NOT NULL), count(*) FILTER (WHERE released_at IS NOT NULL) FROM workspace_content_generation_quota_reservations WHERE attempt_id = $1::uuid`, first.Attempt.ID).Scan(&invoked, &released); err != nil {
		t.Fatal(err)
	}
	if invoked != 1 || released != 0 {
		t.Fatalf("quota reservation invocation=%d release=%d", invoked, released)
	}

	replay, err := service.Generate(context.Background(), subject, input)
	if err != nil || !replay.Replayed || replay.Attempt.ID != first.Attempt.ID || replay.ContentID == nil || *replay.ContentID != *first.ContentID || provider.calls.Load() != 1 {
		t.Fatalf("idempotent replay = %+v calls=%d err=%v", replay, provider.calls.Load(), err)
	}
	var queuePosition sql.NullInt32
	if err := db.QueryRowContext(context.Background(), `SELECT production_queue_position FROM ideas WHERE id = $1::uuid`, sourceIdeaID).Scan(&queuePosition); err != nil {
		t.Fatal(err)
	}
	if queuePosition.Valid {
		t.Fatalf("source Idea queue position after successful Content = %d, want NULL", queuePosition.Int32)
	}
}

func TestGenerationProviderFailureCreatesNoPartialContentAndReleasesOnlyUninvokedReservation(t *testing.T) {
	db := openContentDatabase(t)
	subject, workspaceID, sourceIdeaID, dnaVersionID := createAcceptedContentIdea(t, db)
	provider := &failingContentProvider{category: ai.FailureProviderUnavailable}
	service := content.NewService(db, func(string) (ai.GenerateContentProvider, error) { return provider, nil }, nil, nil)
	result, err := service.Generate(context.Background(), subject, content.ContentGenerationInput{
		WorkspaceID: workspaceID, SourceIdeaID: sourceIdeaID, BaseContentDNAVersionID: dnaVersionID,
		RequestedLanguage: "en", Format: "SHORT_VIDEO", IdempotencyKey: uuid.NewString(),
	})
	if !errors.Is(err, content.ErrGenerationProvider) || result.Attempt.Status != "FAILED" || provider.calls.Load() != 1 {
		t.Fatalf("failed generation result=%+v calls=%d err=%v", result, provider.calls.Load(), err)
	}
	var contents, drafts, versions, released, invoked int
	if err := db.QueryRowContext(context.Background(), `SELECT (SELECT count(*) FROM contents WHERE source_generation_attempt_id = $1::uuid), (SELECT count(*) FROM content_drafts d JOIN contents c ON c.id = d.content_id WHERE c.source_generation_attempt_id = $1::uuid), (SELECT count(*) FROM content_versions v JOIN contents c ON c.id = v.content_id WHERE c.source_generation_attempt_id = $1::uuid), (SELECT count(*) FROM workspace_content_generation_quota_reservations WHERE attempt_id = $1::uuid AND released_at IS NOT NULL), (SELECT count(*) FROM workspace_content_generation_quota_reservations WHERE attempt_id = $1::uuid AND invoked_at IS NOT NULL)`, result.Attempt.ID).Scan(&contents, &drafts, &versions, &released, &invoked); err != nil {
		t.Fatal(err)
	}
	if contents != 0 || drafts != 0 || versions != 0 || released != 0 || invoked != 1 {
		t.Fatalf("failed operation partial rows contents=%d drafts=%d versions=%d released=%d invoked=%d", contents, drafts, versions, released, invoked)
	}
}

func TestGenerationConcurrentReplayDoesNotInvokeProviderTwice(t *testing.T) {
	db := openContentDatabase(t)
	subject, workspaceID, sourceIdeaID, dnaVersionID := createAcceptedContentIdea(t, db)
	generated, err := document.ParseGenerated([]byte(`{"schemaVersion":1,"script":{"blocks":[{"type":"paragraph","text":"Shared result","performanceDirections":[],"editDirections":[]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	provider := &gatedContentProvider{output: &generated, entered: make(chan struct{}), release: make(chan struct{})}
	service := content.NewService(db, func(string) (ai.GenerateContentProvider, error) { return provider, nil }, nil, nil)
	input := content.ContentGenerationInput{
		WorkspaceID: workspaceID, SourceIdeaID: sourceIdeaID, BaseContentDNAVersionID: dnaVersionID,
		RequestedLanguage: "en", Format: "SHORT_VIDEO", IdempotencyKey: uuid.NewString(),
	}
	type response struct {
		result content.ContentGenerationResult
		err    error
	}
	firstResponse := make(chan response, 1)
	go func() {
		result, err := service.Generate(context.Background(), subject, input)
		firstResponse <- response{result: result, err: err}
	}()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first generation did not reach the provider")
	}
	second, err := service.Generate(context.Background(), subject, input)
	if err != nil || !second.Replayed || second.Attempt.Status != "RUNNING" || provider.calls.Load() != 1 {
		t.Fatalf("concurrent replay = %+v calls=%d err=%v", second, provider.calls.Load(), err)
	}
	close(provider.release)
	select {
	case first := <-firstResponse:
		if first.err != nil || first.result.Attempt.Status != "COMPLETED" || first.result.ContentID == nil {
			t.Fatalf("first generation = %+v err=%v", first.result, first.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first generation did not complete")
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want exactly one", provider.calls.Load())
	}
	input.Format = "LONG_VIDEO"
	if _, err := service.Generate(context.Background(), subject, input); !errors.Is(err, content.ErrGenerationConflict) {
		t.Fatalf("changed request reused idempotency key: %v", err)
	}
}

type failingContentProvider struct {
	category ai.FailureCategory
	calls    atomic.Int32
}

func (p *failingContentProvider) GenerateContent(context.Context, ai.ContentRequest) ai.ContentResult {
	p.calls.Add(1)
	return ai.ContentResult{ErrorCategory: p.category}
}

func createAcceptedContentIdea(t *testing.T, db *sql.DB) (contentTestSubject, string, string, string) {
	t.Helper()
	ctx := context.Background()
	userID, workspaceID := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO "user" (id, name, email) VALUES ($1, 'Content test', $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name) VALUES ($1::uuid, 'Content test workspace')`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1::uuid, $2, 'owner')`, workspaceID, userID); err != nil {
		t.Fatal(err)
	}
	cleanupContentWorkspace(t, db, userID, workspaceID)
	dnaService := dna.NewService(db)
	payload := []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Creator"},"audience":{"targetAudienceDescription":"Readers"},"expertise":{"primaryTopics":["Education"]},"voice":{"toneTraits":["Clear"]},"goals":{"contentGoals":["Teach"]},"language":{"defaultContentLanguage":"en","contentLanguages":["en","fa"]}}`)
	dnaVersion, err := dnaService.Save(ctx, contentTestSubject(userID), workspaceID, nil, payload)
	if err != nil {
		t.Fatalf("create AI-ready DNA: %v", err)
	}
	ideaOutput := &ai.Output{SchemaVersion: 1, Ideas: make([]ai.Idea, ai.IdeaGenerationCount)}
	for index := range ideaOutput.Ideas {
		ideaOutput.Ideas[index] = ai.Idea{Title: "Idea " + string(rune('A'+index)), Description: "A useful proposal."}
	}
	ideaProvider := fixedIdeaProvider{output: ideaOutput}
	ideaService := ideas.NewService(db, func(string) (ai.GenerateIdeasProvider, error) { return ideaProvider, nil }, nil, nil)
	batch, err := ideaService.Generate(ctx, contentTestSubject(userID), ideas.GenerationInput{
		WorkspaceID: workspaceID, BaseContentDNAVersionID: dnaVersion.ID, RequestedLanguage: "en", IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create source Ideas: %v", err)
	}
	detail, err := ideaService.GetBatchDetail(ctx, contentTestSubject(userID), workspaceID, batch.Batch.ID)
	if err != nil || len(detail.Ideas) != ai.IdeaGenerationCount {
		t.Fatalf("read source Idea batch: %v; count %d", err, len(detail.Ideas))
	}
	accepted, err := ideaService.UpdateDecision(ctx, contentTestSubject(userID), ideas.DecisionInput{WorkspaceID: workspaceID, IdeaID: detail.Ideas[0].ID, NextState: "ACCEPTED"})
	if err != nil {
		t.Fatalf("accept source Idea: %v", err)
	}
	return contentTestSubject(userID), workspaceID, accepted.ID, dnaVersion.ID
}

func openContentDatabase(t *testing.T) *sql.DB {
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
	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func cleanupContentWorkspace(t *testing.T, db *sql.DB, userID, workspaceID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.ExecContext(ctx, `UPDATE contents SET accepted_version_id = NULL WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM content_versions WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM content_drafts WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM contents WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspace_content_generation_quota_reservations WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM content_generation_attempts WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspace_generation_quota_reservations WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM ideas WHERE batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM idea_generation_batches WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM ai_runs WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM content_dna_versions WHERE content_dna_id IN (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM content_dna WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
}

func equalJSON(left, right any) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	return err == nil && string(leftJSON) == string(rightJSON)
}

var _ workspace.MembershipSubject = contentTestSubject("")
