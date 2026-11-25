package assets

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

var errInvalidStorageKey = errors.New("invalid private Asset storage key")

// stagingKey and permanentKey keep a namespace separate from a caller-controlled
// path. Their zero values are invalid and must be rejected at adapter boundaries.
type stagingKey struct{ id uuid.UUID }
type permanentKey struct{ id uuid.UUID }

func newStagingKey() (stagingKey, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return stagingKey{}, err
	}
	return stagingKey{id: id}, nil
}

func newPermanentKey() (permanentKey, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return permanentKey{}, err
	}
	return permanentKey{id: id}, nil
}

func parseStagingKey(value string) (stagingKey, error) {
	id, err := parseOpaqueStorageKey(value, "staging/")
	if err != nil {
		return stagingKey{}, err
	}
	return stagingKey{id: id}, nil
}

func parsePermanentKey(value string) (permanentKey, error) {
	id, err := parseOpaqueStorageKey(value, "permanent/")
	if err != nil {
		return permanentKey{}, err
	}
	return permanentKey{id: id}, nil
}

func (key stagingKey) String() string {
	if !validUUID(key.id) {
		return ""
	}
	return "staging/" + key.id.String()
}

func (key permanentKey) String() string {
	if !validUUID(key.id) {
		return ""
	}
	return "permanent/" + key.id.String()
}

func (key stagingKey) valid() bool   { return validUUID(key.id) }
func (key permanentKey) valid() bool { return validUUID(key.id) }

func parseOpaqueStorageKey(value, namespace string) (uuid.UUID, error) {
	if !strings.HasPrefix(value, namespace) {
		return uuid.Nil, errInvalidStorageKey
	}
	encodedID := strings.TrimPrefix(value, namespace)
	id, err := uuid.Parse(encodedID)
	if err != nil || id.String() != strings.ToLower(encodedID) || !validUUID(id) {
		return uuid.Nil, errInvalidStorageKey
	}
	return id, nil
}

func validUUID(id uuid.UUID) bool {
	version := uint8(id.Version())
	return id != uuid.Nil && version >= 1 && version <= 8 && id.Variant() == uuid.RFC4122
}
