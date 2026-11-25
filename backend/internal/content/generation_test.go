package content

import (
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestValidateContentGenerationInputCanonicalizesInstructionsAndRequiresUUIDs(t *testing.T) {
	instructions := "\uFEFF  Make the opening clear.  \uFEFF"
	input := ContentGenerationInput{
		WorkspaceID: uuid.NewString(), SourceIdeaID: uuid.NewString(), BaseContentDNAVersionID: uuid.NewString(),
		RequestedLanguage: "fa", Format: "LONG_VIDEO", Instructions: &instructions, IdempotencyKey: uuid.NewString(),
	}
	got, err := validateContentGenerationInput(input)
	if err != nil || got.Instructions == nil || *got.Instructions != "Make the opening clear." {
		t.Fatalf("canonical input = %+v, err=%v", got, err)
	}
	for name, mutate := range map[string]func(*ContentGenerationInput){
		"invalid workspace":       func(value *ContentGenerationInput) { value.WorkspaceID = "not-a-uuid" },
		"invalid source Idea":     func(value *ContentGenerationInput) { value.SourceIdeaID = uuid.Nil.String() },
		"invalid DNA version":     func(value *ContentGenerationInput) { value.BaseContentDNAVersionID = "" },
		"invalid idempotency key": func(value *ContentGenerationInput) { value.IdempotencyKey = "not-a-uuid" },
		"invalid language":        func(value *ContentGenerationInput) { value.RequestedLanguage = "fr" },
		"invalid format":          func(value *ContentGenerationInput) { value.Format = "PODCAST" },
		"blank instructions":      func(value *ContentGenerationInput) { blank := "  \uFEFF "; value.Instructions = &blank },
		"oversized instructions":  func(value *ContentGenerationInput) { long := strings.Repeat("x", 1001); value.Instructions = &long },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := input
			mutate(&candidate)
			if _, err := validateContentGenerationInput(candidate); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestContentGenerationFingerprintUsesRequestFactsNotWorkspaceOrIdempotencyKey(t *testing.T) {
	input := ContentGenerationInput{
		WorkspaceID: uuid.NewString(), SourceIdeaID: uuid.NewString(), BaseContentDNAVersionID: uuid.NewString(),
		RequestedLanguage: "en", Format: "SHORT_VIDEO", IdempotencyKey: uuid.NewString(),
	}
	first, err := validateContentGenerationInput(input)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := fingerprintContentGeneration(first)
	changedScope := first
	changedScope.WorkspaceID = uuid.NewString()
	changedScope.IdempotencyKey = uuid.NewString()
	if got := fingerprintContentGeneration(changedScope); got != fingerprint {
		t.Fatalf("scope and idempotency changed fingerprint: %s != %s", got, fingerprint)
	}
	changedBusinessInput := first
	changedBusinessInput.RequestedLanguage = "fa"
	if got := fingerprintContentGeneration(changedBusinessInput); got == fingerprint {
		t.Fatal("changed request language retained the same fingerprint")
	}
}
