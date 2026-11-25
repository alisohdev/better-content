package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"bettercontent/backend/internal/dna"
)

func TestCanonicalizeIdeaOutputNormalizesAndPreservesOrder(t *testing.T) {
	input, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"ideas": func() []any {
			ideas := make([]any, IdeaGenerationCount)
			for index := range ideas {
				idea := map[string]any{"title": "Idea " + itoa(index+1), "description": "Description " + itoa(index+1)}
				if index == 0 {
					idea["title"] = "\uFEFF First Idea \uFEFF"
					idea["description"] = "  First description\r\nwith a new line  "
					idea["category"] = "  Education  "
				} else if index == 1 {
					idea["category"] = "  "
				}
				ideas[index] = idea
			}
			return ideas
		}(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalizeIdeaOutput(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || len(got.Ideas) != 20 {
		t.Fatalf("canonical output metadata = %+v", got)
	}
	if got.Ideas[0].Title != "First Idea" || got.Ideas[0].Description != "First description\nwith a new line" || got.Ideas[0].Category == nil || *got.Ideas[0].Category != "Education" {
		t.Fatalf("first canonical idea = %+v", got.Ideas[0])
	}
	if got.Ideas[1].Category != nil {
		t.Fatalf("blank category = %#v, want omitted", got.Ideas[1].Category)
	}
}

func TestCanonicalizeIdeaOutputRejectsInvalidAndDuplicateItems(t *testing.T) {
	tests := map[string]func([]map[string]any){
		"wrong count":           func(ideas []map[string]any) { ideas = ideas[:19] },
		"duplicate title":       func(ideas []map[string]any) { ideas[1]["title"] = "IDEA 1" },
		"multiline title":       func(ideas []map[string]any) { ideas[0]["title"] = "two\nlines" },
		"oversized title":       func(ideas []map[string]any) { ideas[0]["title"] = strings.Repeat("x", 121) },
		"oversized description": func(ideas []map[string]any) { ideas[0]["description"] = strings.Repeat("x", 501) },
		"oversized category":    func(ideas []map[string]any) { ideas[0]["category"] = strings.Repeat("x", 81) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input, ideas := makeOutput(t)
			mutate(ideas)
			if name == "wrong count" {
				ideas = ideas[:19]
			}
			input, _ = json.Marshal(map[string]any{"schemaVersion": 1, "ideas": ideas})
			if _, err := CanonicalizeIdeaOutput(input); err == nil {
				t.Fatal("invalid provider result was accepted")
			}
		})
	}
	if _, err := CanonicalizeIdeaOutput([]byte(`{"schemaVersion":1,"ideas":[],"Unexpected":true}`)); err == nil {
		t.Fatal("unknown output field was accepted")
	}
	if _, err := CanonicalizeIdeaOutput([]byte(`{"SchemaVersion":1,"ideas":[]}`)); err == nil {
		t.Fatal("case-insensitive field alias was accepted")
	}
}

func makeOutput(t *testing.T) ([]byte, []map[string]any) {
	t.Helper()
	ideas := make([]map[string]any, IdeaGenerationCount)
	for index := range ideas {
		ideas[index] = map[string]any{"title": "Idea " + itoa(index+1), "description": "Description " + itoa(index+1)}
	}
	input, err := json.Marshal(map[string]any{"schemaVersion": 1, "ideas": ideas})
	if err != nil {
		t.Fatal(err)
	}
	return input, ideas
}

func TestAvalAIUsesApprovedRequestAndReturnsSafeCanonicalOutput(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("request target = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("Authorization header was not set")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Error(err)
		}
		w.Header().Set("avalai-request-id", "request-safe-123")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(validAvalAIResponse()))
	}))
	defer server.Close()
	provider, err := NewAvalAI("user-42", "test-api-key", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	provider.endpoint = server.URL + "/v1/responses"
	provider.client = server.Client()
	request := Request{
		GenerationKind:    IdeaGenerationKind,
		ContentDNA:        readyDNA(),
		RequestedLanguage: "fa",
		RequestedCount:    IdeaGenerationCount,
		PromptVersion:     IdeaGenerationPrompt,
	}
	result := provider.GenerateIdeas(context.Background(), request)
	if !result.Succeeded() || result.Output == nil || len(result.Output.Ideas) != 20 {
		t.Fatalf("provider result = %+v", result)
	}
	if result.ProviderRequestCorrelation != "request-safe-123" {
		t.Fatalf("provider correlation = %q", result.ProviderRequestCorrelation)
	}
	if result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 11 || result.Usage.ReasoningTokens == nil || *result.Usage.ReasoningTokens != 6 {
		t.Fatalf("safe usage = %+v", result.Usage)
	}
	if _, exists := gotBody["tools"]; exists {
		t.Fatal("provider request unexpectedly included tools")
	}
	if gotBody["store"] != false || gotBody["model"] != IdeaGenerationModel || gotBody["max_output_tokens"] != float64(16000) {
		t.Fatalf("request policy = %+v", gotBody)
	}
	if gotBody["safety_identifier"] != safetyIdentifier("user-42", strings.Repeat("s", 32)) {
		t.Fatal("safety identifier was not hashed from the user ID")
	}
	if gotBody["prompt_cache_options"].(map[string]any)["mode"] != "explicit" {
		t.Fatal("prompt cache mode was not explicit")
	}
	input := gotBody["input"].([]any)[0].(map[string]any)["content"].([]any)
	if strings.Contains(input[1].(map[string]any)["text"].(string), "user-42") {
		t.Fatal("request sent the raw user ID")
	}
}

func TestAvalAIFailuresAreMappedWithoutRetainingProviderDetails(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   FailureCategory
	}{
		{name: "rate limit", status: 429, body: `{"error":"private"}`, want: FailureRateLimited},
		{name: "provider unavailable", status: 503, body: `{"error":"private"}`, want: FailureProviderUnavailable},
		{name: "unknown provider status", status: 400, body: `{"error":"private"}`, want: FailureUnknown},
		{name: "refusal", status: 200, body: `{"status":"completed","output":[{"type":"refusal"}],"output_text":"{}"}`, want: FailureInvalidOutput},
		{name: "incomplete", status: 200, body: `{"status":"incomplete","output_text":"{}"}`, want: FailureInvalidOutput},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			provider, err := NewAvalAI("user-42", "test-api-key", strings.Repeat("s", 32))
			if err != nil {
				t.Fatal(err)
			}
			provider.endpoint = server.URL
			provider.client = server.Client()
			result := provider.GenerateIdeas(context.Background(), Request{GenerationKind: IdeaGenerationKind, ContentDNA: readyDNA(), RequestedLanguage: "en", RequestedCount: 20, PromptVersion: IdeaGenerationPrompt})
			if result.ErrorCategory != testCase.want || result.Output != nil {
				t.Fatalf("failure result = %+v, want %s", result, testCase.want)
			}
		})
	}
}

func validAvalAIResponse() string {
	ideas := make([]map[string]any, IdeaGenerationCount)
	for index := range ideas {
		ideas[index] = map[string]any{"title": "Idea " + itoa(index+1), "description": "Description " + itoa(index+1), "category": nil}
	}
	encoded, _ := json.Marshal(map[string]any{
		"status": "completed", "incomplete_details": nil,
		"output":               []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text"}}}},
		"output_text":          mustJSON(map[string]any{"schemaVersion": 1, "ideas": ideas}),
		"usage":                map[string]any{"input_tokens": 11, "output_tokens": 22, "total_tokens": 33, "input_tokens_details": map[string]any{"cached_tokens": 4, "cache_write_tokens": 5}, "output_tokens_details": map[string]any{"reasoning_tokens": 6}, "compute_units": 7, "provider_secret": "discarded"},
		"provider_response_id": "discarded",
	})
	return string(encoded)
}

func readyDNA() dna.Payload {
	parsed, _ := dna.ParsePayload([]byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Creator"},"audience":{"targetAudienceDescription":"Audience"},"expertise":{"primaryTopics":["Education"]},"voice":{"toneTraits":["Warm"]},"goals":{"contentGoals":["Teach"]},"language":{"defaultContentLanguage":"en","contentLanguages":["en","fa"]}}`))
	return parsed
}

func mustJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
func itoa(value int) string     { return strconv.Itoa(value) }
