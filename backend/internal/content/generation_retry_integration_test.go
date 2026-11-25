package content_test

import (
	"context"
	"errors"
	"testing"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/dna"
	"github.com/google/uuid"
)

func TestRetryCreatesNewAttemptWithOriginalRequestAndCurrentDNA(t *testing.T) {
	database := openContentDatabase(t)
	subject, workspaceID, ideaID, dnaVersionID := createAcceptedContentIdea(t, database)
	ctx := context.Background()
	failedProvider := &failingContentProvider{category: ai.FailureProviderUnavailable}
	failedService := content.NewService(database, func(string) (ai.GenerateContentProvider, error) { return failedProvider, nil }, nil, nil)
	failed, err := failedService.Generate(ctx, subject, content.ContentGenerationInput{
		WorkspaceID: workspaceID, SourceIdeaID: ideaID, BaseContentDNAVersionID: dnaVersionID,
		RequestedLanguage: "fa", Format: "LONG_VIDEO", Instructions: stringPointer("Keep the same point of view."), IdempotencyKey: uuid.NewString(),
	})
	if !errors.Is(err, content.ErrGenerationProvider) || failed.Attempt.Status != "FAILED" {
		t.Fatalf("initial failed attempt = %+v, err=%v", failed, err)
	}
	updatedDNA, err := dna.NewService(database).Save(ctx, subject, workspaceID, &dnaVersionID, []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Updated creator"},"audience":{"targetAudienceDescription":"Readers"},"expertise":{"primaryTopics":["Education"]},"voice":{"toneTraits":["Clear"]},"goals":{"contentGoals":["Teach"]},"language":{"defaultContentLanguage":"en","contentLanguages":["en","fa"]}}`))
	if err != nil {
		t.Fatalf("update Content DNA: %v", err)
	}
	output, err := document.ParseGenerated([]byte(`{"schemaVersion":1,"script":{"blocks":[{"type":"paragraph","text":"Retried script.","performanceDirections":[],"editDirections":[]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	retryProvider := &fixedContentProvider{output: &output}
	retryService := content.NewService(database, func(string) (ai.GenerateContentProvider, error) { return retryProvider, nil }, nil, nil)
	retried, err := retryService.Retry(ctx, subject, workspaceID, failed.Attempt.ID)
	if err != nil || retried.Attempt.Status != "COMPLETED" || retried.ContentID == nil || retried.Attempt.ID == failed.Attempt.ID {
		t.Fatalf("retry result = %+v, err=%v", retried, err)
	}
	if retried.Attempt.ContentDNAVersionID != updatedDNA.ID || retried.Attempt.RequestedLanguage != "fa" || retried.Attempt.Format != "LONG_VIDEO" || retried.Attempt.Instructions == nil || *retried.Attempt.Instructions != "Keep the same point of view." {
		t.Fatalf("retry did not preserve request/current DNA: %+v", retried.Attempt)
	}
	if _, err := retryService.Retry(ctx, subject, workspaceID, retried.Attempt.ID); !errors.Is(err, content.ErrGenerationConflict) {
		t.Fatalf("retrying a successful Attempt error = %v, want conflict", err)
	}
	history, err := retryService.IdeaContentGenerationHistory(ctx, subject, workspaceID, ideaID)
	if err != nil || history.SourceIdea.ID != ideaID || !history.IsUsed || len(history.Attempts) != 2 {
		t.Fatalf("Idea Content history = %+v, err=%v", history, err)
	}
	statuses := make(map[string]string, len(history.Attempts))
	for _, attempt := range history.Attempts {
		statuses[attempt.ID] = attempt.Status
	}
	if statuses[failed.Attempt.ID] != "FAILED" || statuses[retried.Attempt.ID] != "COMPLETED" {
		t.Fatalf("attempt history statuses = %#v", statuses)
	}
	failedDetail, err := retryService.ContentGenerationAttemptDetail(ctx, subject, workspaceID, failed.Attempt.ID)
	if err != nil || failedDetail.SourceIdea.ID != ideaID || failedDetail.Attempt.ResultingContentID != nil {
		t.Fatalf("failed Attempt detail = %+v, err=%v", failedDetail, err)
	}
	failedResult, err := retryService.ContentGenerationAttemptResult(ctx, subject, workspaceID, failed.Attempt.ID)
	if err != nil || failedResult != nil {
		t.Fatalf("failed Attempt result = %+v, err=%v", failedResult, err)
	}
	completedDetail, err := retryService.ContentGenerationAttemptDetail(ctx, subject, workspaceID, retried.Attempt.ID)
	if err != nil || completedDetail.Attempt.ResultingContentID == nil || *completedDetail.Attempt.ResultingContentID != *retried.ContentID {
		t.Fatalf("completed Attempt detail = %+v, err=%v", completedDetail, err)
	}
	completedResult, err := retryService.ContentGenerationAttemptResult(ctx, subject, workspaceID, retried.Attempt.ID)
	if err != nil || completedResult == nil || completedResult.ID != *retried.ContentID {
		t.Fatalf("completed Attempt result = %+v, err=%v", completedResult, err)
	}
	usage, err := retryService.IdeaContentUsage(ctx, subject, workspaceID, ideaID)
	if err != nil || !usage.IsUsed || usage.IdeaID != ideaID {
		t.Fatalf("Idea Content usage = %+v, err=%v", usage, err)
	}
	byIdea, err := retryService.ContentByIdea(ctx, subject, workspaceID, ideaID)
	if err != nil || len(byIdea.Content) != 1 || len(byIdea.History.Attempts) != 2 || byIdea.SourceIdea.ID != ideaID {
		t.Fatalf("Content-by-Idea = %+v, err=%v", byIdea, err)
	}
}

func stringPointer(value string) *string { return &value }
