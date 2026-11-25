package document

import (
	"encoding/json"
	"sort"
)

// AssetReference identifies a media attachment owned by one Edit Direction.
type AssetReference struct {
	AssetID       string
	DirectionID   string
	DirectionType string
}

// AssetReferences returns a stable, copy-owned list of references from a
// validated V3/V4 document. Older document variants cannot contain Asset IDs.
func (d Document) AssetReferences() []AssetReference {
	if !d.Valid() || d.SchemaVersion() < 3 {
		return nil
	}
	root := mustDecode(d.JSON())
	script, err := decodeObject(root["script"])
	if err != nil {
		return nil
	}
	blocks, ok := documentObjects(script["blocks"])
	if !ok {
		return nil
	}
	references := make([]AssetReference, 0)
	for _, block := range blocks {
		directions, ok := documentObjects(block["editDirections"])
		if !ok {
			continue
		}
		for _, direction := range directions {
			assetID, hasAsset := stringField(direction, "assetId", true)
			if !hasAsset {
				continue
			}
			directionID, hasID := stringField(direction, "id", true)
			directionType, hasType := stringField(direction, "type", true)
			if hasID && hasType {
				references = append(references, AssetReference{AssetID: assetID, DirectionID: directionID, DirectionType: directionType})
			}
		}
	}
	sort.Slice(references, func(i, j int) bool {
		if references[i].AssetID != references[j].AssetID {
			return references[i].AssetID < references[j].AssetID
		}
		return references[i].DirectionID < references[j].DirectionID
	})
	return references
}

func documentObjects(input []byte) ([]map[string]json.RawMessage, bool) {
	items, ok := rawArray(input)
	if !ok {
		return nil, false
	}
	objects := make([]map[string]json.RawMessage, 0, len(items))
	for _, item := range items {
		object, err := decodeObject(item)
		if err != nil {
			return nil, false
		}
		objects = append(objects, object)
	}
	return objects, true
}
