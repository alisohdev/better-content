package content_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/content/document"
	"github.com/google/uuid"
)

const editedContentDocument = `{"schemaVersion":4,"script":{"blocks":[{"id":"11111111-1111-4111-8111-111111111111","type":"paragraph","text":"Edited script.","performanceDirections":[],"editDirections":[]}]}}`

func TestDraftAcceptanceHistoryAndProductionViewsUseAcceptedSnapshot(t *testing.T) {
	database := openContentDatabase(t)
	subject, workspaceID, ideaID, dnaVersionID := createAcceptedContentIdea(t, database)
	service, contentID := generateContentForTest(t, database, subject, workspaceID, ideaID, dnaVersionID)
	ctx := context.Background()
	target := content.ContentTarget{WorkspaceID: workspaceID, ContentID: contentID}

	draft, err := service.SaveDraft(ctx, subject, target, content.SaveDraftInput{BaseRevision: 1, Document: []byte(editedContentDocument)})
	if err != nil {
		t.Fatalf("save Draft: %v", err)
	}
	if draft.Revision != 2 || !containsJSON(draft.EditorDocument, `"schemaVersion":4`) || !containsJSON(draft.V2Projection, `"schemaVersion":2`) {
		t.Fatalf("saved Draft projections = %+v", draft)
	}
	if _, err := service.SaveDraft(ctx, subject, target, content.SaveDraftInput{BaseRevision: 1, Document: []byte(editedContentDocument)}); !errors.Is(err, content.ErrDraftConflict) {
		t.Fatalf("stale Draft save error = %v, want conflict", err)
	}

	savedDetail, err := service.Detail(ctx, subject, target)
	if err != nil {
		t.Fatalf("read saved Draft: %v", err)
	}
	repeatedDraft, err := service.SaveDraft(ctx, subject, target, content.SaveDraftInput{BaseRevision: 2, Document: []byte(editedContentDocument)})
	if err != nil || repeatedDraft.Revision != draft.Revision || !repeatedDraft.UpdatedAt.Equal(savedDetail.Draft.UpdatedAt) {
		t.Fatalf("equal V4 save changed the Draft: before=%+v after=%+v err=%v", savedDetail.Draft, repeatedDraft, err)
	}

	accepted, err := service.Accept(ctx, subject, target, 2)
	if err != nil {
		t.Fatalf("accept Draft: %v", err)
	}
	if accepted.Draft.Revision != 2 || accepted.AcceptedVersion.VersionNumber != 2 || accepted.AcceptedVersion.Source != "CREATOR_ACCEPTED" || !containsJSON(accepted.AcceptedVersion.Document, `"schemaVersion":4`) {
		t.Fatalf("accepted result = %+v", accepted)
	}
	acceptedAgain, err := service.Accept(ctx, subject, target, 2)
	if err != nil || acceptedAgain.AcceptedVersion.ID != accepted.AcceptedVersion.ID {
		t.Fatalf("repeat acceptance = %+v, err=%v", acceptedAgain, err)
	}

	detail, err := service.Detail(ctx, subject, target)
	if err != nil {
		t.Fatalf("read Content detail: %v", err)
	}
	if detail.AcceptedVersionID == nil || *detail.AcceptedVersionID != accepted.AcceptedVersion.ID || len(detail.Versions) != 2 || !detail.Versions[0].IsCurrentAccepted {
		t.Fatalf("Content history did not point at accepted snapshot: %+v", detail)
	}
	prompt, err := service.Teleprompter(ctx, subject, target)
	if err != nil || prompt.Status != "READY" || prompt.AcceptedVersionNumber == nil || *prompt.AcceptedVersionNumber != 2 || !containsJSONFromAny(prompt.ScriptBlocks, `Edited script.`) {
		t.Fatalf("Teleprompter = %+v, err=%v", prompt, err)
	}
	guide, err := service.EditGuide(ctx, subject, target)
	if err != nil || guide.Status != "READY" || !containsJSONFromAny(guide.ScriptBlocks, `Edited script.`) {
		t.Fatalf("Edit Guide = %+v, err=%v", guide, err)
	}
}

func TestConcurrentDraftSavesPreserveWinningRevision(t *testing.T) {
	database := openContentDatabase(t)
	subject, workspaceID, ideaID, dnaVersionID := createAcceptedContentIdea(t, database)
	service, contentID := generateContentForTest(t, database, subject, workspaceID, ideaID, dnaVersionID)
	ctx := context.Background()
	target := content.ContentTarget{WorkspaceID: workspaceID, ContentID: contentID}
	secondDocument := `{"schemaVersion":4,"script":{"blocks":[{"id":"11111111-1111-4111-8111-111111111112","type":"paragraph","text":"Other edit.","performanceDirections":[],"editDirections":[]}]}}`
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, submitted := range [][]byte{[]byte(editedContentDocument), []byte(secondDocument)} {
		workers.Add(1)
		go func(payload []byte) {
			defer workers.Done()
			<-start
			_, err := service.SaveDraft(ctx, subject, target, content.SaveDraftInput{BaseRevision: 1, Document: payload})
			results <- err
		}(submitted)
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, content.ErrDraftConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Draft save error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent saves produced %d success and %d conflicts", successes, conflicts)
	}

	detail, err := service.Detail(ctx, subject, target)
	if err != nil || detail.Draft.Revision != 2 {
		t.Fatalf("read winning concurrent Draft: detail=%+v err=%v", detail, err)
	}
}

func TestLegacyAcceptanceCreatesCheckpointAndAcceptedVersion(t *testing.T) {
	database := openContentDatabase(t)
	subject, workspaceID, ideaID, dnaVersionID := createAcceptedContentIdea(t, database)
	service, legacyContentID := generateContentForTest(t, database, subject, workspaceID, ideaID, dnaVersionID)
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `UPDATE content_drafts SET document = '{"schemaVersion":1,"script":{"text":"Legacy script."}}'::jsonb, revision = 1 WHERE content_id = $1::uuid`, legacyContentID); err != nil {
		t.Fatal(err)
	}
	legacyTarget := content.ContentTarget{WorkspaceID: workspaceID, ContentID: legacyContentID}
	legacyAccepted, err := service.Accept(ctx, subject, legacyTarget, 1)
	if err != nil {
		t.Fatalf("accept legacy Draft: %v", err)
	}
	if legacyAccepted.Draft.Revision != 2 || legacyAccepted.AcceptedVersion.VersionNumber != 3 {
		t.Fatalf("legacy acceptance = %+v", legacyAccepted)
	}
	var checkpointCount, acceptedCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE source = 'LEGACY_DRAFT_CHECKPOINT'), count(*) FILTER (WHERE source = 'CREATOR_ACCEPTED') FROM content_versions WHERE content_id = $1::uuid`, legacyContentID).Scan(&checkpointCount, &acceptedCount); err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 1 || acceptedCount != 1 {
		t.Fatalf("legacy history checkpoint=%d accepted=%d", checkpointCount, acceptedCount)
	}
}

func generateContentForTest(t *testing.T, database *sql.DB, subject contentTestSubject, workspaceID, ideaID, dnaVersionID string) (*content.Service, string) {
	t.Helper()
	parsed, err := document.ParseGenerated([]byte(`{"schemaVersion":1,"script":{"blocks":[{"type":"paragraph","text":"Generated script.","performanceDirections":[],"editDirections":[]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	provider := &fixedContentProvider{output: &parsed}
	service := content.NewService(database, func(string) (ai.GenerateContentProvider, error) { return provider, nil }, nil, nil)
	result, err := service.Generate(context.Background(), subject, content.ContentGenerationInput{
		WorkspaceID: workspaceID, SourceIdeaID: ideaID, BaseContentDNAVersionID: dnaVersionID,
		RequestedLanguage: "en", Format: "SHORT_VIDEO", IdempotencyKey: uuid.NewString(),
	})
	if err != nil || result.ContentID == nil {
		t.Fatalf("generate test Content: result=%+v err=%v", result, err)
	}
	return service, *result.ContentID
}

func containsJSON(input []byte, substring string) bool {
	return containsJSONFromAny(json.RawMessage(input), substring)
}

func containsJSONFromAny(input any, substring string) bool {
	encoded, err := json.Marshal(input)
	return err == nil && strings.Contains(string(encoded), substring)
}
