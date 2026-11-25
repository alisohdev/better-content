package document

import "testing"

func TestAssetReferencesAreExtractedOncePerOwningEditDirection(t *testing.T) {
	parsed, err := ParseV4([]byte(`{"schemaVersion":4,"script":{"blocks":[{"id":"11111111-1111-4111-8111-111111111111","type":"paragraph","text":"A line","performanceDirections":[],"editDirections":[{"id":"33333333-3333-4333-8333-333333333333","type":"BROLL_CUE","description":"A street","searchQuery":"city street","assetId":"55555555-5555-4555-8555-555555555555"},{"id":"22222222-2222-4222-8222-222222222222","type":"SOUND_CUE","kind":"music","description":"Soft music","assetId":"44444444-4444-4444-8444-444444444444"}] }]}}`))
	if err != nil {
		t.Fatal(err)
	}
	references := parsed.Document().AssetReferences()
	if len(references) != 2 || references[0].AssetID != "44444444-4444-4444-8444-444444444444" || references[0].DirectionID != "22222222-2222-4222-8222-222222222222" || references[0].DirectionType != "SOUND_CUE" || references[1].AssetID != "55555555-5555-4555-8555-555555555555" || references[1].DirectionID != "33333333-3333-4333-8333-333333333333" || references[1].DirectionType != "BROLL_CUE" {
		t.Fatalf("asset references = %+v", references)
	}
	references[0].AssetID = "mutated"
	if parsed.Document().AssetReferences()[0].AssetID == "mutated" {
		t.Fatal("asset references were not returned by copy")
	}
}
