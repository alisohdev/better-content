package ai

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/platform/textnorm"
)

const (
	IdeaGenerationKind         = "IDEA_GENERATION"
	IdeaGenerationPrompt       = "idea-generation/v1"
	IdeaGenerationModel        = "gpt-5.6-luna"
	IdeaGenerationProvider     = "avalai"
	IdeaGenerationSchema       = "idea_generation_v1"
	IdeaGenerationCount        = 20
	IdeaGenerationTimeoutSecs  = 60
	IdeaGenerationOutputTokens = 16000
)

type FailureCategory string

const (
	FailureTimeout             FailureCategory = "TIMEOUT"
	FailureRateLimited         FailureCategory = "RATE_LIMITED"
	FailureProviderUnavailable FailureCategory = "PROVIDER_UNAVAILABLE"
	FailureInvalidOutput       FailureCategory = "INVALID_OUTPUT"
	FailureInterrupted         FailureCategory = "INTERRUPTED"
	FailureUnknown             FailureCategory = "UNKNOWN"
)

type Usage struct {
	InputTokens       *int64 `json:"inputTokens,omitempty"`
	OutputTokens      *int64 `json:"outputTokens,omitempty"`
	TotalTokens       *int64 `json:"totalTokens,omitempty"`
	CachedInputTokens *int64 `json:"cachedInputTokens,omitempty"`
	CacheWriteTokens  *int64 `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens   *int64 `json:"reasoningTokens,omitempty"`
	ComputeUnits      *int64 `json:"computeUnits,omitempty"`
}

type Settings struct {
	StructuredOutput struct {
		SchemaName    string `json:"schemaName"`
		SchemaVersion int    `json:"schemaVersion"`
	} `json:"structuredOutput"`
	ReasoningEffort string `json:"reasoningEffort"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	RetryPolicy     struct {
		MaxRetries int `json:"maxRetries"`
	} `json:"retryPolicy"`
	ServiceTier string `json:"serviceTier"`
}

type Idea struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Category    *string `json:"category,omitempty"`
}

type Output struct {
	SchemaVersion int    `json:"schemaVersion"`
	Ideas         []Idea `json:"ideas"`
}

type Request struct {
	GenerationKind    string
	ContentDNA        dna.Payload
	RequestedLanguage string
	RequestedCount    int
	PromptVersion     string
}

type Result struct {
	Output                     *Output
	Usage                      *Usage
	ProviderRequestCorrelation string
	ErrorCategory              FailureCategory
}

func (r Result) Succeeded() bool { return r.Output != nil && r.ErrorCategory == "" }

type GenerateIdeasProvider interface {
	GenerateIdeas(context.Context, Request) Result
}

var ErrInvalidIdeaOutput = errors.New("canonical idea output is invalid")

// CanonicalizeIdeaOutput is the only trust boundary for provider-generated ideas.
func CanonicalizeIdeaOutput(input []byte) (Output, error) {
	if err := validateOutputShape(input); err != nil {
		return Output{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	var output Output
	if err := decoder.Decode(&output); err != nil {
		return Output{}, ErrInvalidIdeaOutput
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Output{}, ErrInvalidIdeaOutput
	}
	if output.SchemaVersion != 1 || len(output.Ideas) != IdeaGenerationCount {
		return Output{}, ErrInvalidIdeaOutput
	}
	seen := make(map[string]struct{}, IdeaGenerationCount)
	for index := range output.Ideas {
		idea := &output.Ideas[index]
		idea.Title = trimJS(idea.Title)
		idea.Description = trimJS(strings.ReplaceAll(strings.ReplaceAll(idea.Description, "\r\n", "\n"), "\r", "\n"))
		if idea.Category != nil {
			category := trimJS(*idea.Category)
			if category == "" {
				idea.Category = nil
			} else {
				idea.Category = &category
			}
		}
		if !validLength(idea.Title, 120) || strings.ContainsAny(idea.Title, "\r\n") ||
			!validLength(idea.Description, 500) ||
			(idea.Category != nil && (!validLength(*idea.Category, 80) || strings.ContainsAny(*idea.Category, "\r\n"))) {
			return Output{}, ErrInvalidIdeaOutput
		}
		key := strings.ToLower(idea.Title)
		if _, exists := seen[key]; exists {
			return Output{}, ErrInvalidIdeaOutput
		}
		seen[key] = struct{}{}
	}
	return output, nil
}

func validateOutputShape(input []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(input, &root); err != nil || root == nil {
		return ErrInvalidIdeaOutput
	}
	if err := validateKeys(root, "schemaVersion", "ideas"); err != nil {
		return err
	}
	if _, exists := root["schemaVersion"]; !exists {
		return ErrInvalidIdeaOutput
	}
	var rawIdeas []json.RawMessage
	if err := json.Unmarshal(root["ideas"], &rawIdeas); err != nil || rawIdeas == nil {
		return ErrInvalidIdeaOutput
	}
	for _, rawIdea := range rawIdeas {
		var idea map[string]json.RawMessage
		if err := json.Unmarshal(rawIdea, &idea); err != nil || idea == nil {
			return ErrInvalidIdeaOutput
		}
		if err := validateKeys(idea, "title", "description", "category"); err != nil {
			return err
		}
		if _, exists := idea["title"]; !exists {
			return ErrInvalidIdeaOutput
		}
		if _, exists := idea["description"]; !exists {
			return ErrInvalidIdeaOutput
		}
	}
	return nil
}

func validateKeys(object map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range object {
		if _, exists := set[key]; !exists {
			return ErrInvalidIdeaOutput
		}
	}
	return nil
}

func validateRequest(request Request) bool {
	return request.GenerationKind == IdeaGenerationKind && request.RequestedCount == IdeaGenerationCount &&
		(request.RequestedLanguage == "en" || request.RequestedLanguage == "fa") &&
		request.PromptVersion == IdeaGenerationPrompt && request.ContentDNA.Readiness() == dna.AIReady
}

func safetyIdentifier(userID, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(userID))
	return hex.EncodeToString(mac.Sum(nil))
}

func parseSafeUsage(raw json.RawMessage) *Usage {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil
	}
	inputDetails, _ := objectField(fields["input_tokens_details"])
	outputDetails, _ := objectField(fields["output_tokens_details"])
	usage := &Usage{
		InputTokens:       safeNumber(fields["input_tokens"]),
		OutputTokens:      safeNumber(fields["output_tokens"]),
		TotalTokens:       safeNumber(fields["total_tokens"]),
		CachedInputTokens: safeNumber(inputDetails["cached_tokens"]),
		CacheWriteTokens:  safeNumber(inputDetails["cache_write_tokens"]),
		ReasoningTokens:   safeNumber(outputDetails["reasoning_tokens"]),
		ComputeUnits:      safeNumber(fields["compute_units"]),
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.TotalTokens == nil &&
		usage.CachedInputTokens == nil && usage.CacheWriteTokens == nil && usage.ReasoningTokens == nil && usage.ComputeUnits == nil {
		return nil
	}
	return usage
}

func safeNumber(raw json.RawMessage) *int64 {
	if len(raw) == 0 {
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return nil
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || value < 0 || value > 9007199254740991 || math.Trunc(value) != value {
		return nil
	}
	integer := int64(value)
	return &integer
}

func objectField(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func trimJS(value string) string {
	return textnorm.TrimECMAScript(value)
}

func validLength(value string, maximum int) bool {
	length := len(utf16.Encode([]rune(value)))
	return length >= 1 && length <= maximum
}
