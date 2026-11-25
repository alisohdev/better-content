package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"bettercontent/backend/internal/platform/textnorm"
	"github.com/google/uuid"
)

var ErrInvalidDocument = errors.New("content document is invalid")

const (
	maxScriptCharacters = 50_000
	maxBlocks           = 1_000
	maxDirections       = 300
	maxBlockDirections  = 12
)

// Document is an immutable, validated persisted Content document. Its JSON is
// kept private so callers cannot change data after it passes the parser.
type Document struct {
	version int
	encoded []byte
}

// V4 is a validated canonical editor document. Only the package parser can
// create a non-zero value.
type V4 struct{ document Document }

func (d Document) SchemaVersion() int { return d.version }

func (d Document) Valid() bool { return d.version >= 1 && d.version <= 4 && len(d.encoded) > 0 }

func (d Document) JSON() []byte { return bytes.Clone(d.encoded) }

func (d Document) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return nil, ErrInvalidDocument
	}
	return bytes.Clone(d.encoded), nil
}

func (v V4) Valid() bool { return v.document.Valid() && v.document.version == 4 }

func (v V4) Document() Document {
	if !v.Valid() {
		return Document{}
	}
	return Document{version: 4, encoded: bytes.Clone(v.document.encoded)}
}

func (v V4) MarshalJSON() ([]byte, error) {
	if !v.Valid() {
		return nil, ErrInvalidDocument
	}
	return bytes.Clone(v.document.encoded), nil
}

// ParseStored validates a persisted V1–V4 shape. Structured versions are
// canonicalized in memory; V1 remains byte-for-byte represented as returned by
// PostgreSQL so a legacy checkpoint can preserve the stored artifact.
func ParseStored(input []byte) (Document, error) {
	root, err := object(input)
	if err != nil || checkKeys(root, "schemaVersion", "script") != nil {
		return Document{}, ErrInvalidDocument
	}
	version, ok := integer(root["schemaVersion"])
	if !ok || version < 1 || version > 4 {
		return Document{}, ErrInvalidDocument
	}
	if err := validateDocument(root, version); err != nil {
		return Document{}, ErrInvalidDocument
	}
	if version == 1 {
		return Document{version: version, encoded: bytes.Clone(input)}, nil
	}
	canonical, err := canonicalizeStructured(root, version)
	if err != nil {
		return Document{}, ErrInvalidDocument
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return Document{}, ErrInvalidDocument
	}
	return Document{version: version, encoded: encoded}, nil
}

func ParseV4(input []byte) (V4, error) {
	document, err := ParseStored(input)
	if err != nil || document.version != 4 {
		return V4{}, ErrInvalidDocument
	}
	return V4{document: document}, nil
}

// ParseSubmitted applies the same strict union validation as reads. The
// application service decides which submitted schema version is valid for the
// currently stored Draft before persisting it.
func ParseSubmitted(input []byte) (Document, error) { return ParseStored(input) }

func ProjectToV4(document Document) (V4, error) {
	if !document.Valid() {
		return V4{}, ErrInvalidDocument
	}
	value, err := decodeObject(document.encoded)
	if err != nil {
		return V4{}, ErrInvalidDocument
	}
	if document.version == 1 {
		value = materializeV1(value)
	}
	blocks, ok := arrayOfObjects(value["script"], "blocks")
	if !ok {
		return V4{}, ErrInvalidDocument
	}
	for _, block := range blocks {
		directions, ok := rawArray(block["editDirections"])
		if !ok {
			return V4{}, ErrInvalidDocument
		}
		if document.version < 4 {
			for index, raw := range directions {
				direction, err := decodeObject(raw)
				if err != nil {
					return V4{}, ErrInvalidDocument
				}
				delete(direction, "searchQuery")
				directions[index], err = json.Marshal(direction)
				if err != nil {
					return V4{}, ErrInvalidDocument
				}
			}
		}
		block["editDirections"], _ = json.Marshal(directions)
	}
	if err := writeBlocks(value, blocks); err != nil {
		return V4{}, ErrInvalidDocument
	}
	value["schemaVersion"], _ = json.Marshal(4)
	encoded, err := json.Marshal(value)
	if err != nil {
		return V4{}, ErrInvalidDocument
	}
	parsed, err := ParseStored(encoded)
	if err != nil {
		return V4{}, err
	}
	return V4{document: parsed}, nil
}

func ProjectToV2(document Document) (Document, error) {
	if !document.Valid() {
		return Document{}, ErrInvalidDocument
	}
	if document.version == 1 {
		v2 := materializeV1(mustDecode(document.encoded))
		encoded, err := json.Marshal(v2)
		if err != nil {
			return Document{}, ErrInvalidDocument
		}
		return ParseStored(encoded)
	}
	value := mustDecode(document.encoded)
	blocks, ok := arrayOfObjects(value["script"], "blocks")
	if !ok {
		return Document{}, ErrInvalidDocument
	}
	for _, block := range blocks {
		directions, ok := rawArray(block["editDirections"])
		if !ok {
			return Document{}, ErrInvalidDocument
		}
		for index, raw := range directions {
			direction, err := decodeObject(raw)
			if err != nil {
				return Document{}, ErrInvalidDocument
			}
			delete(direction, "assetId")
			delete(direction, "searchQuery")
			directions[index], err = json.Marshal(direction)
			if err != nil {
				return Document{}, ErrInvalidDocument
			}
		}
		block["editDirections"], _ = json.Marshal(directions)
	}
	if err := writeBlocks(value, blocks); err != nil {
		return Document{}, ErrInvalidDocument
	}
	value["schemaVersion"], _ = json.Marshal(2)
	encoded, err := json.Marshal(value)
	if err != nil {
		return Document{}, ErrInvalidDocument
	}
	return ParseStored(encoded)
}

func Equal(left, right Document) bool {
	if !left.Valid() || !right.Valid() {
		return false
	}
	if left.version == 2 && right.version == 3 {
		projected, err := ProjectVersion(left, 3)
		return err == nil && Equal(projected, right)
	}
	if left.version == 3 && right.version == 2 {
		projected, err := ProjectVersion(right, 3)
		return err == nil && Equal(left, projected)
	}
	if left.version == 3 && right.version == 4 {
		projected, err := ProjectVersion(left, 4)
		return err == nil && Equal(projected, right)
	}
	if left.version == 4 && right.version == 3 {
		projected, err := ProjectVersion(right, 4)
		return err == nil && Equal(left, projected)
	}
	if left.version != right.version {
		return false
	}
	var a, b any
	if json.Unmarshal(left.encoded, &a) != nil || json.Unmarshal(right.encoded, &b) != nil {
		return false
	}
	return deepEqual(a, b)
}

func ProjectVersion(document Document, version int) (Document, error) {
	if !document.Valid() || version < 2 || version > 4 {
		return Document{}, ErrInvalidDocument
	}
	value := mustDecode(document.encoded)
	if version == 2 {
		projected, err := ProjectToV2(document)
		return projected, err
	}
	if document.version == 1 {
		value = materializeV1(value)
	}
	if version == 4 && document.version < 4 {
		blocks, ok := arrayOfObjects(value["script"], "blocks")
		if !ok {
			return Document{}, ErrInvalidDocument
		}
		for _, block := range blocks {
			directions, ok := rawArray(block["editDirections"])
			if !ok {
				return Document{}, ErrInvalidDocument
			}
			for index, raw := range directions {
				direction, err := decodeObject(raw)
				if err != nil {
					return Document{}, ErrInvalidDocument
				}
				delete(direction, "searchQuery")
				directions[index], err = json.Marshal(direction)
				if err != nil {
					return Document{}, ErrInvalidDocument
				}
			}
			block["editDirections"], _ = json.Marshal(directions)
		}
		if err := writeBlocks(value, blocks); err != nil {
			return Document{}, ErrInvalidDocument
		}
	} else if version == 3 && document.version == 4 {
		blocks, ok := arrayOfObjects(value["script"], "blocks")
		if !ok {
			return Document{}, ErrInvalidDocument
		}
		for _, block := range blocks {
			directions, ok := rawArray(block["editDirections"])
			if !ok {
				return Document{}, ErrInvalidDocument
			}
			for index, raw := range directions {
				direction, err := decodeObject(raw)
				if err != nil {
					return Document{}, ErrInvalidDocument
				}
				delete(direction, "searchQuery")
				directions[index], err = json.Marshal(direction)
				if err != nil {
					return Document{}, ErrInvalidDocument
				}
			}
			block["editDirections"], _ = json.Marshal(directions)
		}
		if err := writeBlocks(value, blocks); err != nil {
			return Document{}, ErrInvalidDocument
		}
	}
	value["schemaVersion"], _ = json.Marshal(version)
	encoded, err := json.Marshal(value)
	if err != nil {
		return Document{}, ErrInvalidDocument
	}
	return ParseStored(encoded)
}

func validateDocument(root map[string]json.RawMessage, version int) error {
	if err := checkKeys(root, "schemaVersion", "script"); err != nil {
		return err
	}
	script, err := decodeObject(root["script"])
	if err != nil {
		return err
	}
	if version == 1 {
		if err := checkKeys(script, "text"); err != nil {
			return err
		}
		text, ok := stringField(script, "text", true)
		if !ok || utf16Length(text) > maxScriptCharacters {
			return ErrInvalidDocument
		}
		return nil
	}
	if err := checkKeys(script, "blocks"); err != nil {
		return err
	}
	blocks, ok := rawArray(script["blocks"])
	if !ok || len(blocks) == 0 || len(blocks) > maxBlocks {
		return ErrInvalidDocument
	}
	blockIDs := make(map[string]struct{}, len(blocks))
	directionIDs := make(map[string]struct{})
	characters, directionsCount := 0, 0
	for _, rawBlock := range blocks {
		block, err := decodeObject(rawBlock)
		if err != nil || checkKeys(block, "id", "type", "text", "performanceDirections", "editDirections") != nil {
			return ErrInvalidDocument
		}
		id, ok := stringField(block, "id", true)
		if !ok || !validUUID(id) {
			return ErrInvalidDocument
		}
		if _, exists := blockIDs[id]; exists {
			return ErrInvalidDocument
		}
		blockIDs[id] = struct{}{}
		kind, ok := stringField(block, "type", true)
		if !ok || kind != "paragraph" {
			return ErrInvalidDocument
		}
		text, ok := stringField(block, "text", true)
		if !ok || strings.ContainsAny(text, "\r\n") {
			return ErrInvalidDocument
		}
		characters += utf16Length(text)
		for _, field := range []struct{ name, category string }{{"performanceDirections", "performance"}, {"editDirections", "edit"}} {
			items, ok := rawArray(block[field.name])
			if !ok || len(items) > maxBlockDirections {
				return ErrInvalidDocument
			}
			directionsCount += len(items)
			for _, rawDirection := range items {
				if err := validateDirection(rawDirection, field.category, version, directionIDs); err != nil {
					return err
				}
			}
		}
	}
	if characters > maxScriptCharacters || directionsCount > maxDirections {
		return ErrInvalidDocument
	}
	return nil
}

func validateDirection(raw json.RawMessage, category string, version int, seen map[string]struct{}) error {
	value, err := decodeObject(raw)
	if err != nil {
		return ErrInvalidDocument
	}
	typeName, ok := stringField(value, "type", true)
	if !ok {
		return ErrInvalidDocument
	}
	id, ok := stringField(value, "id", true)
	if !ok || !validUUID(id) {
		return ErrInvalidDocument
	}
	if _, exists := seen[id]; exists {
		return ErrInvalidDocument
	}
	seen[id] = struct{}{}
	fields := map[string][]string{
		"PAUSE": {"duration"}, "EMPHASIS": {"strength"}, "DELIVERY": {"tone", "pace"},
		"GESTURE": {"kind"}, "POSITION": {"action"}, "GAZE": {"target"}, "PERFORMANCE_NOTE": {"text"},
		"TEXT_OVERLAY": {"text", "placement"}, "ZOOM": {"mode", "intensity"}, "CUT": {"style"},
		"BROLL_CUE": {"description"}, "SOUND_CUE": {"kind", "description"},
		"CAPTION_EMPHASIS": {"style"}, "EDIT_NOTE": {"text"},
	}
	allowed, exists := fields[typeName]
	if !exists || (category == "performance") != isPerformance(typeName) {
		return ErrInvalidDocument
	}
	keys := []string{"id", "type"}
	keys = append(keys, allowed...)
	if hasField(typeName, "nuance") {
		keys = append(keys, "nuance")
	}
	if version >= 3 && (typeName == "BROLL_CUE" || typeName == "SOUND_CUE") {
		keys = append(keys, "assetId")
	}
	if version == 4 && typeName == "BROLL_CUE" {
		keys = append(keys, "searchQuery")
	}
	if checkKeys(value, keys...) != nil {
		return ErrInvalidDocument
	}
	if nuance, present := value["nuance"]; present {
		var text string
		if json.Unmarshal(nuance, &text) != nil || utf16Length(text) > 280 {
			return ErrInvalidDocument
		}
	}
	for field, maximum := range map[string]int{"text": 500, "description": 500} {
		if typeName == "TEXT_OVERLAY" {
			maximum = 280
		}
		if typeName == "SOUND_CUE" && field == "description" {
			maximum = 280
		}
		if rawValue, present := value[field]; present {
			var text string
			if json.Unmarshal(rawValue, &text) != nil || utf16Length(text) > maximum {
				return ErrInvalidDocument
			}
		}
	}
	for _, field := range allowed {
		rawValue, present := value[field]
		if !present && typeName == "DELIVERY" && (field == "tone" || field == "pace") {
			continue
		}
		if !present {
			return ErrInvalidDocument
		}
		var text string
		if json.Unmarshal(rawValue, &text) != nil {
			return ErrInvalidDocument
		}
		if field == "tone" || field == "pace" {
			if text == "" && typeName == "DELIVERY" {
				continue
			}
		}
		if !validDirectionValue(typeName, field, text) {
			return ErrInvalidDocument
		}
	}
	if typeName == "DELIVERY" {
		tone, hasTone := value["tone"]
		pace, hasPace := value["pace"]
		if !nonEmptyString(tone) && !nonEmptyString(pace) {
			return ErrInvalidDocument
		}
		if hasTone && !nonEmptyString(tone) || hasPace && !nonEmptyString(pace) {
			return ErrInvalidDocument
		}
	}
	if asset, present := value["assetId"]; present {
		var assetID string
		if json.Unmarshal(asset, &assetID) != nil || !validUUID(assetID) {
			return ErrInvalidDocument
		}
	}
	if query, present := value["searchQuery"]; present {
		var text string
		if json.Unmarshal(query, &text) != nil || utf8.RuneCountInString(text) > 200 || text == "" || textnorm.TrimECMAScript(text) != text || strings.ContainsAny(text, "\r\n") {
			return ErrInvalidDocument
		}
	}
	return nil
}

func canonicalizeStructured(root map[string]json.RawMessage, version int) (map[string]json.RawMessage, error) {
	copyRoot := cloneObject(root)
	script, err := decodeObject(copyRoot["script"])
	if err != nil {
		return nil, ErrInvalidDocument
	}
	encodedScript, err := json.Marshal(script)
	if err != nil {
		return nil, ErrInvalidDocument
	}
	blocks, ok := arrayOfObjects(encodedScript, "blocks")
	if !ok {
		return nil, ErrInvalidDocument
	}
	kept := make([]map[string]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		text, _ := stringField(block, "text", true)
		performance, _ := rawArray(block["performanceDirections"])
		edit, _ := rawArray(block["editDirections"])
		if textnorm.TrimECMAScript(text) != "" || len(performance) > 0 || len(edit) > 0 {
			kept = append(kept, block)
		}
	}
	if len(kept) == 0 {
		first := cloneObject(blocks[0])
		first["text"], _ = json.Marshal("")
		first["performanceDirections"], _ = json.Marshal([]any{})
		first["editDirections"], _ = json.Marshal([]any{})
		kept = []map[string]json.RawMessage{first}
	}
	encodedBlocks, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	script["blocks"] = encodedBlocks
	copyRoot["script"], err = json.Marshal(script)
	if err != nil {
		return nil, err
	}
	return copyRoot, nil
}

func materializeV1(root map[string]json.RawMessage) map[string]json.RawMessage {
	script, _ := decodeObject(root["script"])
	var text string
	_ = json.Unmarshal(script["text"], &text)
	lines := strings.Split(text, "\n")
	blocks := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if textnorm.TrimECMAScript(line) == "" {
			continue
		}
		blocks = append(blocks, map[string]any{"id": deterministicBlockID(line, len(blocks)), "type": "paragraph", "text": line, "performanceDirections": []any{}, "editDirections": []any{}})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"id": deterministicBlockID("", 0), "type": "paragraph", "text": "", "performanceDirections": []any{}, "editDirections": []any{}})
	}
	result := map[string]json.RawMessage{}
	result["schemaVersion"], _ = json.Marshal(2)
	result["script"], _ = json.Marshal(map[string]any{"blocks": blocks})
	return result
}

func deterministicBlockID(text string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("content-document-v1-block:%d:%s", index, text)))
	var raw [16]byte
	copy(raw[:], digest[:16])
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(raw[0:4]), hex.EncodeToString(raw[4:6]), hex.EncodeToString(raw[6:8]), hex.EncodeToString(raw[8:10]), hex.EncodeToString(raw[10:16]))
}

func isPerformance(kind string) bool {
	switch kind {
	case "PAUSE", "EMPHASIS", "DELIVERY", "GESTURE", "POSITION", "GAZE", "PERFORMANCE_NOTE":
		return true
	}
	return false
}

func validDirectionValue(kind, field, value string) bool {
	values := map[string][]string{
		"PAUSE.duration":         {"short", "medium", "long"},
		"EMPHASIS.strength":      {"subtle", "clear", "strong"},
		"DELIVERY.tone":          {"calm", "warm", "serious", "energetic", "playful"},
		"DELIVERY.pace":          {"slower", "faster"},
		"GESTURE.kind":           {"hand", "point", "show_object"},
		"POSITION.action":        {"sit", "stand", "walk"},
		"GAZE.target":            {"camera", "away"},
		"TEXT_OVERLAY.placement": {"top", "center", "bottom"},
		"ZOOM.mode":              {"in", "out"}, "ZOOM.intensity": {"subtle", "normal", "strong"},
		"CUT.style": {"hard", "jump"}, "SOUND_CUE.kind": {"music", "sound_effect"},
		"CAPTION_EMPHASIS.style": {"highlight", "animate"},
	}
	allowed := values[kind+"."+field]
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func hasField(kind, field string) bool { return (kind != "PERFORMANCE_NOTE" && kind != "EDIT_NOTE") }

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == strings.ToLower(value)
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func object(input []byte) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(input, &value); err != nil || value == nil {
		return nil, ErrInvalidDocument
	}
	return value, nil
}

func decodeObject(input json.RawMessage) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if len(input) == 0 || json.Unmarshal(input, &value) != nil || value == nil {
		return nil, ErrInvalidDocument
	}
	return value, nil
}

func rawArray(input json.RawMessage) ([]json.RawMessage, bool) {
	var value []json.RawMessage
	if len(input) == 0 || bytes.Equal(bytes.TrimSpace(input), []byte("null")) || json.Unmarshal(input, &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func arrayOfObjects(input json.RawMessage, field string) ([]map[string]json.RawMessage, bool) {
	object, err := decodeObject(input)
	if err != nil {
		return nil, false
	}
	items, ok := rawArray(object[field])
	if !ok {
		return nil, false
	}
	result := make([]map[string]json.RawMessage, 0, len(items))
	for _, item := range items {
		parsed, err := decodeObject(item)
		if err != nil {
			return nil, false
		}
		result = append(result, parsed)
	}
	return result, true
}

func checkKeys(value map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range value {
		if _, ok := set[key]; !ok {
			return ErrInvalidDocument
		}
	}
	return nil
}

func stringField(value map[string]json.RawMessage, key string, required bool) (string, bool) {
	raw, exists := value[key]
	if !exists {
		return "", !required
	}
	var result string
	if json.Unmarshal(raw, &result) != nil {
		return "", false
	}
	return result, true
}

func integer(raw json.RawMessage) (int, bool) {
	var result float64
	err := json.Unmarshal(raw, &result)
	if err != nil || result < 0 || result > 4 || result != float64(int(result)) {
		return 0, false
	}
	return int(result), true
}

func nonEmptyString(raw json.RawMessage) bool {
	var result string
	return json.Unmarshal(raw, &result) == nil && result != ""
}

func cloneObject(value map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(value))
	for key, raw := range value {
		result[key] = bytes.Clone(raw)
	}
	return result
}

func writeBlocks(root map[string]json.RawMessage, blocks []map[string]json.RawMessage) error {
	script, err := decodeObject(root["script"])
	if err != nil {
		return err
	}
	encodedBlocks, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	script["blocks"] = encodedBlocks
	root["script"], err = json.Marshal(script)
	return err
}

func mustDecode(input []byte) map[string]json.RawMessage {
	result, _ := object(input)
	return result
}

func deepEqual(left, right any) bool {
	leftJSON, errLeft := json.Marshal(left)
	rightJSON, errRight := json.Marshal(right)
	return errLeft == nil && errRight == nil && bytes.Equal(leftJSON, rightJSON)
}
