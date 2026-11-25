package dna

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf16"
)

type Readiness string

const (
	Incomplete Readiness = "INCOMPLETE"
	AIReady    Readiness = "AI_READY"
)

var ErrInvalidPayload = errors.New("content DNA payload is invalid")

type Payload struct {
	SchemaVersion int          `json:"schemaVersion"`
	Identity      *Identity    `json:"identity,omitempty"`
	Audience      *Audience    `json:"audience,omitempty"`
	Expertise     *Expertise   `json:"expertise,omitempty"`
	Voice         *Voice       `json:"voice,omitempty"`
	Goals         *Goals       `json:"goals,omitempty"`
	Preferences   *Preferences `json:"preferences,omitempty"`
	Language      *Language    `json:"language,omitempty"`
}

type Identity struct {
	CreatorOrBrandDescription *string `json:"creatorOrBrandDescription,omitempty"`
}
type Audience struct {
	TargetAudienceDescription *string `json:"targetAudienceDescription,omitempty"`
}
type Expertise struct {
	PrimaryTopics []string `json:"primaryTopics,omitempty"`
}
type Voice struct {
	ToneTraits     []string `json:"toneTraits,omitempty"`
	PreferredStyle *string  `json:"preferredStyle,omitempty"`
}
type Goals struct {
	ContentGoals []string `json:"contentGoals,omitempty"`
}
type Preferences struct {
	PreferredFormats       []string `json:"preferredFormats,omitempty"`
	TopicsToAvoid          []string `json:"topicsToAvoid,omitempty"`
	ApproachesToAvoid      []string `json:"approachesToAvoid,omitempty"`
	AdditionalInstructions *string  `json:"additionalInstructions,omitempty"`
}
type Language struct {
	DefaultContentLanguage *string  `json:"defaultContentLanguage,omitempty"`
	ContentLanguages       []string `json:"contentLanguages,omitempty"`
}

// ParsePayload is the sole boundary for Content DNA input and stored snapshots.
// It rejects unknown fields and returns a canonical, storage-valid snapshot.
func ParsePayload(input []byte) (Payload, error) {
	if err := validatePayloadShape(input); err != nil {
		return Payload{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	var payload Payload
	if err := decoder.Decode(&payload); err != nil {
		return Payload{}, ErrInvalidPayload
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Payload{}, ErrInvalidPayload
	}
	if payload.SchemaVersion != 1 {
		return Payload{}, ErrInvalidPayload
	}

	var err error
	if payload.Identity != nil {
		payload.Identity.CreatorOrBrandDescription = normalizeText(payload.Identity.CreatorOrBrandDescription)
		if !validText(payload.Identity.CreatorOrBrandDescription, 1500) {
			return Payload{}, ErrInvalidPayload
		}
		if payload.Identity.CreatorOrBrandDescription == nil {
			payload.Identity = nil
		}
	}
	if payload.Audience != nil {
		payload.Audience.TargetAudienceDescription = normalizeText(payload.Audience.TargetAudienceDescription)
		if !validText(payload.Audience.TargetAudienceDescription, 1500) {
			return Payload{}, ErrInvalidPayload
		}
		if payload.Audience.TargetAudienceDescription == nil {
			payload.Audience = nil
		}
	}
	if payload.Expertise != nil {
		payload.Expertise.PrimaryTopics, err = normalizeList(payload.Expertise.PrimaryTopics, 10, 80)
		if err != nil {
			return Payload{}, err
		}
		if len(payload.Expertise.PrimaryTopics) == 0 {
			payload.Expertise = nil
		}
	}
	if payload.Voice != nil {
		payload.Voice.ToneTraits, err = normalizeList(payload.Voice.ToneTraits, 5, 60)
		if err != nil {
			return Payload{}, err
		}
		payload.Voice.PreferredStyle = normalizeText(payload.Voice.PreferredStyle)
		if !validText(payload.Voice.PreferredStyle, 1200) {
			return Payload{}, ErrInvalidPayload
		}
		if len(payload.Voice.ToneTraits) == 0 && payload.Voice.PreferredStyle == nil {
			payload.Voice = nil
		}
	}
	if payload.Goals != nil {
		payload.Goals.ContentGoals, err = normalizeList(payload.Goals.ContentGoals, 5, 120)
		if err != nil {
			return Payload{}, err
		}
		if len(payload.Goals.ContentGoals) == 0 {
			payload.Goals = nil
		}
	}
	if payload.Preferences != nil {
		p := payload.Preferences
		if p.PreferredFormats, err = normalizeList(p.PreferredFormats, 8, 80); err != nil {
			return Payload{}, err
		}
		if p.TopicsToAvoid, err = normalizeList(p.TopicsToAvoid, 10, 120); err != nil {
			return Payload{}, err
		}
		if p.ApproachesToAvoid, err = normalizeList(p.ApproachesToAvoid, 10, 160); err != nil {
			return Payload{}, err
		}
		p.AdditionalInstructions = normalizeText(p.AdditionalInstructions)
		if !validText(p.AdditionalInstructions, 2000) {
			return Payload{}, ErrInvalidPayload
		}
		if len(p.PreferredFormats)+len(p.TopicsToAvoid)+len(p.ApproachesToAvoid) == 0 && p.AdditionalInstructions == nil {
			payload.Preferences = nil
		}
	}
	if payload.Language != nil {
		l := payload.Language
		l.DefaultContentLanguage = normalizeText(l.DefaultContentLanguage)
		if l.DefaultContentLanguage != nil && *l.DefaultContentLanguage != "en" && *l.DefaultContentLanguage != "fa" {
			return Payload{}, ErrInvalidPayload
		}
		l.ContentLanguages, err = normalizeList(l.ContentLanguages, 2, 2)
		if err != nil {
			return Payload{}, err
		}
		for _, language := range l.ContentLanguages {
			if language != "en" && language != "fa" {
				return Payload{}, ErrInvalidPayload
			}
		}
		if l.DefaultContentLanguage != nil && !contains(l.ContentLanguages, *l.DefaultContentLanguage) {
			return Payload{}, ErrInvalidPayload
		}
		if l.DefaultContentLanguage == nil && len(l.ContentLanguages) == 0 {
			payload.Language = nil
		}
	}
	return payload, nil
}

func validatePayloadShape(input []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(input, &root); err != nil || root == nil {
		return ErrInvalidPayload
	}
	if err := validateObjectKeys(root, "schemaVersion", "identity", "audience", "expertise", "voice", "goals", "preferences", "language"); err != nil {
		return err
	}
	objects := map[string][]string{
		"identity":    {"creatorOrBrandDescription"},
		"audience":    {"targetAudienceDescription"},
		"expertise":   {"primaryTopics"},
		"voice":       {"toneTraits", "preferredStyle"},
		"goals":       {"contentGoals"},
		"preferences": {"preferredFormats", "topicsToAvoid", "approachesToAvoid", "additionalInstructions"},
		"language":    {"defaultContentLanguage", "contentLanguages"},
	}
	for field, allowed := range objects {
		raw, exists := root[field]
		if !exists {
			continue
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err != nil || nested == nil {
			return ErrInvalidPayload
		}
		if err := validateObjectKeys(nested, allowed...); err != nil {
			return err
		}
		for _, value := range nested {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return ErrInvalidPayload
			}
		}
	}
	if raw, exists := root["schemaVersion"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalidPayload
	}
	return nil
}

func validateObjectKeys(object map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range object {
		if _, ok := set[key]; !ok {
			return ErrInvalidPayload
		}
	}
	return nil
}

func (p Payload) Readiness() Readiness {
	if p.Identity == nil || p.Identity.CreatorOrBrandDescription == nil || p.Audience == nil || p.Audience.TargetAudienceDescription == nil || p.Expertise == nil || len(p.Expertise.PrimaryTopics) == 0 || p.Voice == nil || len(p.Voice.ToneTraits) == 0 || p.Goals == nil || len(p.Goals.ContentGoals) == 0 || p.Language == nil || p.Language.DefaultContentLanguage == nil || !contains(p.Language.ContentLanguages, *p.Language.DefaultContentLanguage) {
		return Incomplete
	}
	return AIReady
}

func normalizeText(value *string) *string {
	if value == nil {
		return nil
	}
	text := trimText(strings.ReplaceAll(strings.ReplaceAll(*value, "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return nil
	}
	return &text
}

func validText(value *string, max int) bool {
	return value == nil || (utf16Length(*value) > 0 && utf16Length(*value) <= max)
}

func normalizeList(values []string, maxCount, maxLength int) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := trimText(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
		if normalized == "" || utf16Length(normalized) > maxLength {
			return nil, ErrInvalidPayload
		}
		key := strings.ToLower(normalized)
		if !seen[key] {
			seen[key] = true
			result = append(result, normalized)
		}
	}
	if len(result) > maxCount {
		return nil, ErrInvalidPayload
	}
	return result, nil
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func trimText(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}
