package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAvalAIGeneratesCanonicalContentWithApprovedRequest(t *testing.T) {
	generated := map[string]any{"schemaVersion": 1, "script": map[string]any{"blocks": []any{
		map[string]any{"type": "paragraph", "text": "  Hello creator.  ", "performanceDirections": []any{}, "editDirections": []any{}},
	}}}
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("request target = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Error("Authorization header was not set")
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		w.Header().Set("avalai-request-id", "content-request-safe-123")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mustJSON(map[string]any{
			"status": "completed", "incomplete_details": nil,
			"output":      []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text"}}}},
			"output_text": mustJSON(generated), "usage": map[string]any{"input_tokens": 5, "output_tokens": 8},
		})))
	}))
	defer server.Close()

	provider, err := NewAvalAI("user-private", "test-api-key", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	if provider.client.Timeout != 90*time.Second {
		t.Fatalf("HTTP client timeout = %s, want 90s", provider.client.Timeout)
	}
	provider.endpoint = server.URL + "/v1/responses"
	provider.client = server.Client()
	instructions := "Mention the opening idea."
	result := provider.GenerateContent(context.Background(), ContentRequest{
		GenerationKind: ContentGenerationKind,
		SourceIdea:     SourceIdea{Title: "Opening", Description: "Explain a useful point", Category: stringPointer("Education")},
		ContentDNA:     readyDNA(), RequestedLanguage: "en", Format: "SHORT_VIDEO", Instructions: &instructions,
	})
	if !result.Succeeded() || result.Output == nil {
		t.Fatalf("content result = %+v", result)
	}
	if result.ProviderRequestCorrelation != "content-request-safe-123" || result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 5 {
		t.Fatalf("provider metadata = %+v", result)
	}
	var materialized struct {
		SchemaVersion int `json:"schemaVersion"`
		Script        struct {
			Blocks []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"script"`
	}
	if err := json.Unmarshal(result.Output.Document().JSON(), &materialized); err != nil {
		t.Fatal(err)
	}
	if materialized.SchemaVersion != 4 || len(materialized.Script.Blocks) != 1 || materialized.Script.Blocks[0].ID == "" || materialized.Script.Blocks[0].Text != "Hello creator." {
		t.Fatalf("materialized document = %+v", materialized)
	}
	if requestBody["model"] != IdeaGenerationModel || requestBody["store"] != false || requestBody["max_output_tokens"] != float64(ContentGenerationOutputTokens) {
		t.Fatalf("provider policy = %+v", requestBody)
	}
	if requestBody["safety_identifier"] != safetyIdentifier("user-private", strings.Repeat("s", 32)) {
		t.Fatal("safety identifier was not derived from the user ID")
	}
	format := requestBody["text"].(map[string]any)["format"].(map[string]any)
	if format["name"] != ContentGenerationSchema || format["strict"] != true {
		t.Fatalf("structured output configuration = %+v", format)
	}
	encodedSchema, _ := json.Marshal(format["schema"])
	if strings.Contains(string(encodedSchema), "assetId") || strings.Contains(string(encodedSchema), `"id"`) {
		t.Fatalf("provider schema must not accept generated IDs or assets: %s", encodedSchema)
	}
}

func TestAvalAIContentRejectsInvalidRequestsAndProviderOutputSafely(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		want   FailureCategory
		status int
	}{
		{name: "invalid output", body: `{"status":"completed","incomplete_details":null,"output_text":"{\"schemaVersion\":1,\"script\":{\"blocks\":[]}}"}`, want: FailureInvalidOutput},
		{name: "refusal", body: `{"status":"completed","refusal":"no","output_text":"{}"}`, want: FailureInvalidOutput},
		{name: "rate limit", body: `{"error":"private"}`, want: FailureRateLimited, status: http.StatusTooManyRequests},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			status := testCase.status
			if status == 0 {
				status = http.StatusOK
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			provider, err := NewAvalAI("user", "key", strings.Repeat("s", 32))
			if err != nil {
				t.Fatal(err)
			}
			provider.endpoint = server.URL
			provider.client = server.Client()
			request := validContentRequest()
			result := provider.GenerateContent(context.Background(), request)
			if result.ErrorCategory != testCase.want || result.Output != nil {
				t.Fatalf("result = %+v, want safe failure %q", result, testCase.want)
			}
		})
	}

	provider, err := NewAvalAI("user", "key", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	request := validContentRequest()
	request.Instructions = stringPointer("  not canonical  ")
	if result := provider.GenerateContent(context.Background(), request); result.ErrorCategory != FailureInvalidOutput {
		t.Fatalf("invalid request result = %+v", result)
	}
}

func validContentRequest() ContentRequest {
	return ContentRequest{
		GenerationKind: ContentGenerationKind,
		SourceIdea:     SourceIdea{Title: "Idea", Description: "Description"},
		ContentDNA:     readyDNA(), RequestedLanguage: "fa", Format: "LONG_VIDEO",
	}
}

func stringPointer(value string) *string { return &value }
