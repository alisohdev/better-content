package document

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	blockID = "11111111-1111-4111-8111-111111111111"
	dirID   = "22222222-2222-4222-8222-222222222222"
)

func TestParseStoredCanonicalizesStructuredDocumentsAndKeepsVersion(t *testing.T) {
	input := `{"schemaVersion":4,"script":{"blocks":[{"id":"` + blockID + `","type":"paragraph","text":"  line  ","performanceDirections":[],"editDirections":[{"id":"` + dirID + `","type":"BROLL_CUE","description":"show","searchQuery":"cafe","nuance":"warm"}]},{"id":"33333333-3333-4333-8333-333333333333","type":"paragraph","text":" \t ","performanceDirections":[],"editDirections":[]}]}}`
	document, err := ParseStored([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion() != 4 {
		t.Fatalf("schema version = %d", document.SchemaVersion())
	}
	var decoded struct {
		Script struct {
			Blocks []struct {
				Text           string `json:"text"`
				EditDirections []struct {
					SearchQuery string `json:"searchQuery"`
				} `json:"editDirections"`
			} `json:"blocks"`
		} `json:"script"`
	}
	if err := json.Unmarshal(document.JSON(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Script.Blocks) != 1 || decoded.Script.Blocks[0].Text != "  line  " {
		t.Fatalf("canonical blocks = %#v", decoded.Script.Blocks)
	}
	if got := decoded.Script.Blocks[0].EditDirections[0].SearchQuery; got != "cafe" {
		t.Fatalf("persisted V4 query unexpectedly changed: %q", got)
	}
}

func TestParseStoredRejectsUnknownFieldsDuplicateIDsAndWrongDirectionValues(t *testing.T) {
	valid := `{"schemaVersion":2,"script":{"blocks":[{"id":"` + blockID + `","type":"paragraph","text":"line","performanceDirections":[{"id":"` + dirID + `","type":"PAUSE","duration":"short"}],"editDirections":[]}]}}`
	cases := map[string]string{
		"unknown field":          strings.Replace(valid, `"text":"line"`, `"text":"line","extra":true`, 1),
		"duplicate direction id": strings.Replace(valid, `"editDirections":[]`, `"editDirections":[{"id":"`+dirID+`","type":"EDIT_NOTE","text":"note"}]`, 1),
		"wrong enum":             strings.Replace(valid, `"duration":"short"`, `"duration":"forever"`, 1),
		"wrong category":         strings.Replace(valid, `"performanceDirections":[{"id":"`+dirID+`","type":"PAUSE","duration":"short"}]`, `"performanceDirections":[{"id":"`+dirID+`","type":"EDIT_NOTE","text":"note"}]`, 1),
		"unexpected null asset":  strings.Replace(valid, `"editDirections":[]`, `"editDirections":[{"id":"33333333-3333-4333-8333-333333333333","type":"BROLL_CUE","description":"cue","assetId":null}]`, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseStored([]byte(input)); err == nil {
				t.Fatal("expected invalid document")
			}
		})
	}
}

func TestProjectionPreservesV4SearchAndDropsItForLegacyViews(t *testing.T) {
	input := `{"schemaVersion":4,"script":{"blocks":[{"id":"` + blockID + `","type":"paragraph","text":"script","performanceDirections":[],"editDirections":[{"id":"` + dirID + `","type":"BROLL_CUE","description":"visual","assetId":"33333333-3333-4333-8333-333333333333","searchQuery":"coffee shop"}]}]}}`
	v4, err := ParseStored([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	projected, err := ProjectToV4(v4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projected.Document().JSON()), `"searchQuery":"coffee shop"`) {
		t.Fatalf("V4 projection lost search query: %s", projected.Document().JSON())
	}
	legacyJSON := strings.Replace(input, `"schemaVersion":4`, `"schemaVersion":3`, 1)
	legacyJSON = strings.Replace(legacyJSON, `,"searchQuery":"coffee shop"`, "", 1)
	if _, err := ParseStored([]byte(legacyJSON)); err != nil {
		t.Fatalf("equivalent V3 projection invalid: %s: %v", legacyJSON, err)
	}
	v3, err := ProjectVersion(v4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v3.JSON()), "searchQuery") {
		t.Fatalf("V3 projection retained V4 query: %s", v3.JSON())
	}
	v2, err := ProjectToV2(v4)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v2.JSON()), "assetId") || strings.Contains(string(v2.JSON()), "searchQuery") {
		t.Fatalf("V2 projection retained newer fields: %s", v2.JSON())
	}
	if Equal(v2, v4) {
		t.Fatal("direct V2-to-V4 equality must remain false")
	}
}

func TestV1EditorProjectionUsesStableIDsAndPreservesLineWhitespace(t *testing.T) {
	input := []byte(`{"schemaVersion":1,"script":{"text":"  first  \n \t \nsecond"}}`)
	v1, err := ParseStored(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ProjectToV4(v1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProjectToV4(v1)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Document().JSON()) != string(second.Document().JSON()) {
		t.Fatal("legacy IDs are not deterministic")
	}
	var value struct {
		Script struct {
			Blocks []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"script"`
	}
	if err := json.Unmarshal(first.Document().JSON(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Script.Blocks) != 2 || value.Script.Blocks[0].Text != "  first  " || value.Script.Blocks[1].Text != "second" {
		t.Fatalf("V1 projection = %#v", value.Script.Blocks)
	}
	if value.Script.Blocks[0].ID != "" && !validUUID(value.Script.Blocks[0].ID) {
		t.Fatalf("materialized block id is invalid: %q", value.Script.Blocks[0].ID)
	}
}

func TestTextLimitsMatchJavaScriptLengthUnits(t *testing.T) {
	accepted := strings.Repeat("😀", maxScriptCharacters/2)
	rejected := strings.Repeat("😀", maxScriptCharacters/2+1)
	if _, err := ParseStored([]byte(`{"schemaVersion":1,"script":{"text":"` + accepted + `"}}`)); err != nil {
		t.Fatalf("50,000 UTF-16-unit text rejected: %v", err)
	}
	if _, err := ParseStored([]byte(`{"schemaVersion":1,"script":{"text":"` + rejected + `"}}`)); err == nil {
		t.Fatal("text longer than 50,000 UTF-16 units was accepted")
	}
}

func TestParseGeneratedValidatesThenMaterializesOneCanonicalV4(t *testing.T) {
	input := []byte(`{"schemaVersion":1,"script":{"blocks":[{"type":"paragraph","text":"  opening  ","performanceDirections":[{"type":"PAUSE","duration":"short"}],"editDirections":[{"type":"BROLL_CUE","description":"show a cup","searchQuery":"ceramic cup"}]}]}}`)
	generated, err := ParseGenerated(input)
	if err != nil {
		t.Fatal(err)
	}
	if !generated.Valid() {
		t.Fatal("generated V4 is invalid")
	}
	var value struct {
		SchemaVersion int `json:"schemaVersion"`
		Script        struct {
			Blocks []struct {
				ID          string `json:"id"`
				Text        string `json:"text"`
				Performance []struct {
					ID string `json:"id"`
				} `json:"performanceDirections"`
				Edit []struct {
					ID          string `json:"id"`
					SearchQuery string `json:"searchQuery"`
				} `json:"editDirections"`
			} `json:"blocks"`
		} `json:"script"`
	}
	if err := json.Unmarshal(generated.Document().JSON(), &value); err != nil {
		t.Fatal(err)
	}
	if value.SchemaVersion != 4 || len(value.Script.Blocks) != 1 || value.Script.Blocks[0].Text != "opening" {
		t.Fatalf("generated document = %#v", value)
	}
	block := value.Script.Blocks[0]
	if !validUUID(block.ID) || len(block.Performance) != 1 || !validUUID(block.Performance[0].ID) || len(block.Edit) != 1 || !validUUID(block.Edit[0].ID) {
		t.Fatalf("materialized IDs = %#v", block)
	}
	if block.Edit[0].SearchQuery != "ceramic cup" {
		t.Fatalf("B-roll search query = %q", block.Edit[0].SearchQuery)
	}
}

func TestParseGeneratedRejectsProviderReferencesAndInvalidSearchText(t *testing.T) {
	template := `{"schemaVersion":1,"script":{"blocks":[{"type":"paragraph","text":"script","performanceDirections":[],"editDirections":[{"type":"BROLL_CUE","description":"cue","searchQuery":%q}]}]}}`
	for _, query := range []string{"https://example.com", "www.example.com", "Pexels ID 123", "  query  ", strings.Repeat("😀", 201)} {
		t.Run(query, func(t *testing.T) {
			if _, err := ParseGenerated([]byte(fmt.Sprintf(template, query))); err == nil {
				t.Fatalf("accepted unsafe query %q", query)
			}
		})
	}
	withClientIDs := `{"schemaVersion":1,"script":{"blocks":[{"id":"` + blockID + `","type":"paragraph","text":"script","performanceDirections":[],"editDirections":[]}]}}`
	if _, err := ParseGenerated([]byte(withClientIDs)); err == nil {
		t.Fatal("accepted provider-supplied persistent block ID")
	}
}

func TestTeleprompterAndEditGuideUseExactLegacyOrStructuredDirections(t *testing.T) {
	legacy, err := ParseStored([]byte(`{"schemaVersion":1,"script":{"text":" exact legacy text "}}`))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := Teleprompter(legacy)
	if err != nil || len(prompt) != 1 || prompt[0].ID != "legacy-script" || prompt[0].Text != " exact legacy text " {
		t.Fatalf("legacy teleprompter = %#v, err=%v", prompt, err)
	}
	guide, err := EditGuide(legacy)
	if err != nil || len(guide) != 1 || guide[0].ID != "legacy-script" || len(guide[0].EditDirections) != 0 {
		t.Fatalf("legacy edit guide = %#v, err=%v", guide, err)
	}

	structured, err := ParseStored([]byte(`{"schemaVersion":4,"script":{"blocks":[{"id":"` + blockID + `","type":"paragraph","text":"spoken","performanceDirections":[{"id":"` + dirID + `","type":"DELIVERY","tone":"warm","pace":"slower","nuance":"gently"}],"editDirections":[{"id":"33333333-3333-4333-8333-333333333333","type":"BROLL_CUE","description":"a cup","searchQuery":"ceramic cup"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err = Teleprompter(structured)
	if err != nil || len(prompt) != 1 || len(prompt[0].PerformanceHints) != 1 {
		t.Fatalf("structured teleprompter = %#v, err=%v", prompt, err)
	}
	hint := prompt[0].PerformanceHints[0]
	if hint.Type != "DELIVERY" || len(hint.Values) != 2 || hint.Values[0] != "warm" || hint.Values[1] != "slower" || hint.Nuance == nil || *hint.Nuance != "gently" {
		t.Fatalf("delivery hint = %#v", hint)
	}
	guide, err = EditGuide(structured)
	if err != nil || len(guide) != 1 || len(guide[0].EditDirections) != 1 || !strings.Contains(string(guide[0].EditDirections[0]), `"searchQuery":"ceramic cup"`) {
		t.Fatalf("structured edit guide = %#v, err=%v", guide, err)
	}
}
