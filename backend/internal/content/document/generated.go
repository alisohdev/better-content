package document

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"bettercontent/backend/internal/platform/textnorm"
	"github.com/google/uuid"
)

var providerAssetReference = regexp.MustCompile(`(?i)(?:pexels|pixabay|unsplash|pinterest)\s*(?:id|[0-9]+)`)

// ParseGenerated validates the provider's schema-version-1 payload, normalizes
// generated text, assigns persistent IDs locally, and returns one canonical V4
// value. Call it once, then use that value for Draft, Version 1 and AI snapshot.
func ParseGenerated(input []byte) (V4, error) {
	root, err := object(input)
	if err != nil || checkKeys(root, "schemaVersion", "script") != nil {
		return V4{}, ErrInvalidDocument
	}
	version, ok := integer(root["schemaVersion"])
	if !ok || version != 1 {
		return V4{}, ErrInvalidDocument
	}
	script, err := decodeObject(root["script"])
	if err != nil || checkKeys(script, "blocks") != nil {
		return V4{}, ErrInvalidDocument
	}
	blocks, ok := rawArray(script["blocks"])
	if !ok || len(blocks) == 0 || len(blocks) > maxBlocks {
		return V4{}, ErrInvalidDocument
	}
	materialized := make([]map[string]json.RawMessage, 0, len(blocks))
	for _, rawBlock := range blocks {
		block, err := decodeObject(rawBlock)
		if err != nil || checkKeys(block, "type", "text", "performanceDirections", "editDirections") != nil {
			return V4{}, ErrInvalidDocument
		}
		kind, ok := stringField(block, "type", true)
		if !ok || kind != "paragraph" {
			return V4{}, ErrInvalidDocument
		}
		text, ok := stringField(block, "text", true)
		if !ok {
			return V4{}, ErrInvalidDocument
		}
		text = textnorm.TrimECMAScript(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
		if text == "" || utf16Length(text) > maxScriptCharacters || strings.ContainsAny(text, "\r\n") {
			return V4{}, ErrInvalidDocument
		}
		block["text"], _ = json.Marshal(text)
		block["id"], _ = json.Marshal(uuid.NewString())
		for _, field := range []struct{ name, category string }{{"performanceDirections", "performance"}, {"editDirections", "edit"}} {
			directions, ok := rawArray(block[field.name])
			if !ok || len(directions) > maxBlockDirections {
				return V4{}, ErrInvalidDocument
			}
			materializedDirections := make([]json.RawMessage, 0, len(directions))
			for _, rawDirection := range directions {
				direction, err := decodeObject(rawDirection)
				if err != nil || hasKey(direction, "id") || hasKey(direction, "assetId") {
					return V4{}, ErrInvalidDocument
				}
				if field.category == "edit" {
					typeName, _ := stringField(direction, "type", true)
					if typeName == "BROLL_CUE" {
						query, ok := stringField(direction, "searchQuery", true)
						if !ok || !plainSearchQuery(query) {
							return V4{}, ErrInvalidDocument
						}
						description, ok := stringField(direction, "description", true)
						if !ok || description == "" {
							return V4{}, ErrInvalidDocument
						}
					}
				}
				direction["id"], _ = json.Marshal(uuid.NewString())
				encodedDirection, err := json.Marshal(direction)
				if err != nil || validateDirection(encodedDirection, field.category, 4, make(map[string]struct{})) != nil {
					return V4{}, ErrInvalidDocument
				}
				materializedDirections = append(materializedDirections, encodedDirection)
			}
			block[field.name], _ = json.Marshal(materializedDirections)
		}
		materialized = append(materialized, block)
	}
	value := map[string]json.RawMessage{}
	value["schemaVersion"], _ = json.Marshal(4)
	value["script"], _ = json.Marshal(map[string]any{"blocks": materialized})
	encoded, err := json.Marshal(value)
	if err != nil {
		return V4{}, ErrInvalidDocument
	}
	return ParseV4(encoded)
}

func plainSearchQuery(value string) bool {
	if utf8.RuneCountInString(value) > 200 || value == "" || textnorm.TrimECMAScript(value) != value || strings.ContainsAny(value, "\r\n") {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.Contains(lower, "http://") && !strings.Contains(lower, "https://") &&
		!strings.Contains(lower, "www.") && !providerAssetReference.MatchString(value)
}

func hasKey(value map[string]json.RawMessage, key string) bool {
	_, exists := value[key]
	return exists
}
