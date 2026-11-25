package dna

import (
	"encoding/json"
	"testing"
)

func TestParsePayloadCanonicalizesAndDerivesReadiness(t *testing.T) {
	input := []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"  Creator\r\n  "},"expertise":{"primaryTopics":[" Strategy ","strategy","Storytelling"]},"language":{"defaultContentLanguage":"en","contentLanguages":["en","fa"]}}`)
	got, err := ParsePayload(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Identity == nil || *got.Identity.CreatorOrBrandDescription != "Creator" {
		t.Fatalf("identity canonical value = %#v", got.Identity)
	}
	if got.Expertise == nil || len(got.Expertise.PrimaryTopics) != 2 || got.Expertise.PrimaryTopics[0] != "Strategy" {
		t.Fatalf("topics = %#v", got.Expertise)
	}
	if got.Readiness() != Incomplete {
		t.Fatalf("readiness = %s, want incomplete", got.Readiness())
	}
	serialized, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(serialized) != `{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Creator"},"expertise":{"primaryTopics":["Strategy","Storytelling"]},"language":{"defaultContentLanguage":"en","contentLanguages":["en","fa"]}}` {
		t.Fatalf("canonical payload = %s", serialized)
	}
	bom, err := ParsePayload([]byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"\ufeffCreator\ufeff"}}`))
	if err != nil || bom.Identity == nil || *bom.Identity.CreatorOrBrandDescription != "Creator" {
		t.Fatalf("ECMAScript whitespace normalization = %+v, err=%v", bom, err)
	}
}

func TestParsePayloadReturnsAIReadyOnlyForRequiredFields(t *testing.T) {
	got, err := ParsePayload([]byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Creator"},"audience":{"targetAudienceDescription":"Audience"},"expertise":{"primaryTopics":["Topic"]},"voice":{"toneTraits":["Practical"]},"goals":{"contentGoals":["Teach"]},"language":{"defaultContentLanguage":"fa","contentLanguages":["fa"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Readiness() != AIReady {
		t.Fatalf("readiness = %s, want AI_READY", got.Readiness())
	}
}

func TestParsePayloadRejectsInvalidShapeAndLimits(t *testing.T) {
	tests := map[string]string{
		"unsupported schema":         `{"schemaVersion":2}`,
		"unknown field":              `{"schemaVersion":1,"unapproved":true}`,
		"case-sensitive field names": `{"SchemaVersion":1}`,
		"null optional section":      `{"schemaVersion":1,"identity":null}`,
		"null optional property":     `{"schemaVersion":1,"identity":{"creatorOrBrandDescription":null}}`,
		"blank list entry":           `{"schemaVersion":1,"expertise":{"primaryTopics":["  "]}}`,
		"oversized description":      `{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"` + repeat("x", 1501) + `"}}`,
		"unsupported language":       `{"schemaVersion":1,"language":{"contentLanguages":["de"]}}`,
		"default not allowed":        `{"schemaVersion":1,"language":{"defaultContentLanguage":"fa","contentLanguages":["en"]}}`,
		"too many unique topics":     `{"schemaVersion":1,"expertise":{"primaryTopics":["a","b","c","d","e","f","g","h","i","j","k"]}}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePayload([]byte(input)); err == nil {
				t.Fatal("ParsePayload accepted invalid input")
			}
		})
	}
}

func repeat(value string, count int) string {
	out := ""
	for range count {
		out += value
	}
	return out
}
