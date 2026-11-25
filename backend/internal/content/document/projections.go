package document

import "encoding/json"

type TeleprompterHint struct {
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Text   *string  `json:"text,omitempty"`
	Nuance *string  `json:"nuance,omitempty"`
}

type TeleprompterBlock struct {
	ID               string             `json:"id"`
	Text             string             `json:"text"`
	PerformanceHints []TeleprompterHint `json:"performanceHints"`
}

type EditGuideBlock struct {
	ID             string            `json:"id"`
	Text           string            `json:"text"`
	EditDirections []json.RawMessage `json:"editDirections"`
}

func Teleprompter(value Document) ([]TeleprompterBlock, error) {
	if !value.Valid() {
		return nil, ErrInvalidDocument
	}
	root := mustDecode(value.encoded)
	if value.version == 1 {
		script, err := decodeObject(root["script"])
		if err != nil {
			return nil, ErrInvalidDocument
		}
		text, ok := stringField(script, "text", true)
		if !ok {
			return nil, ErrInvalidDocument
		}
		return []TeleprompterBlock{{ID: "legacy-script", Text: text, PerformanceHints: []TeleprompterHint{}}}, nil
	}
	blocks, ok := arrayOfObjects(root["script"], "blocks")
	if !ok {
		return nil, ErrInvalidDocument
	}
	result := make([]TeleprompterBlock, 0, len(blocks))
	for _, block := range blocks {
		id, _ := stringField(block, "id", true)
		text, _ := stringField(block, "text", true)
		rawHints, ok := rawArray(block["performanceDirections"])
		if !ok {
			return nil, ErrInvalidDocument
		}
		hints := make([]TeleprompterHint, 0, len(rawHints))
		for _, raw := range rawHints {
			hint, err := teleprompterHint(raw)
			if err != nil {
				return nil, ErrInvalidDocument
			}
			hints = append(hints, hint)
		}
		result = append(result, TeleprompterBlock{ID: id, Text: text, PerformanceHints: hints})
	}
	return result, nil
}

func EditGuide(value Document) ([]EditGuideBlock, error) {
	if !value.Valid() {
		return nil, ErrInvalidDocument
	}
	root := mustDecode(value.encoded)
	if value.version == 1 {
		script, err := decodeObject(root["script"])
		if err != nil {
			return nil, ErrInvalidDocument
		}
		text, ok := stringField(script, "text", true)
		if !ok {
			return nil, ErrInvalidDocument
		}
		return []EditGuideBlock{{ID: "legacy-script", Text: text, EditDirections: []json.RawMessage{}}}, nil
	}
	blocks, ok := arrayOfObjects(root["script"], "blocks")
	if !ok {
		return nil, ErrInvalidDocument
	}
	result := make([]EditGuideBlock, 0, len(blocks))
	for _, block := range blocks {
		id, _ := stringField(block, "id", true)
		text, _ := stringField(block, "text", true)
		directions, ok := rawArray(block["editDirections"])
		if !ok {
			return nil, ErrInvalidDocument
		}
		copied := make([]json.RawMessage, len(directions))
		for index, direction := range directions {
			copied[index] = append(json.RawMessage(nil), direction...)
		}
		result = append(result, EditGuideBlock{ID: id, Text: text, EditDirections: copied})
	}
	return result, nil
}

func teleprompterHint(raw json.RawMessage) (TeleprompterHint, error) {
	value, err := decodeObject(raw)
	if err != nil {
		return TeleprompterHint{}, err
	}
	id, _ := stringField(value, "id", true)
	typeName, _ := stringField(value, "type", true)
	values := make([]string, 0, 2)
	var text, nuance *string
	add := func(field string) error {
		value, exists := stringField(value, field, false)
		if !exists {
			return ErrInvalidDocument
		}
		if value != "" {
			values = append(values, value)
		}
		return nil
	}
	for _, field := range []string{"duration", "strength", "tone", "pace", "kind", "action", "target"} {
		if _, present := value[field]; present {
			if err := add(field); err != nil {
				return TeleprompterHint{}, err
			}
		}
	}
	if rawText, present := value["text"]; present {
		parsed, ok := stringField(value, "text", true)
		if !ok {
			return TeleprompterHint{}, ErrInvalidDocument
		}
		text = &parsed
		_ = rawText
	}
	if _, present := value["nuance"]; present {
		parsed, ok := stringField(value, "nuance", true)
		if !ok {
			return TeleprompterHint{}, ErrInvalidDocument
		}
		nuance = &parsed
	}
	return TeleprompterHint{ID: id, Type: typeName, Values: values, Text: text, Nuance: nuance}, nil
}
