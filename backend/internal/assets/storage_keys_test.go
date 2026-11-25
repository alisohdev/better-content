package assets

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseStorageKeysAcceptCanonicalRFCUUIDsAcrossSupportedVersions(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"staging/f47ac10b-58cc-11cf-a447-001122334455",
		"staging/d9428888-122b-4a7e-a35a-0e1001016d98",
		"staging/01890f3e-1ba7-8be2-8c0a-000000000001",
	} {
		key, err := parseStagingKey(value)
		if err != nil {
			t.Fatalf("parseStagingKey(%q): %v", value, err)
		}
		if key.String() != strings.ToLower(value) {
			t.Fatalf("key.String() = %q, want %q", key.String(), strings.ToLower(value))
		}
	}

	uppercase := "permanent/D9428888-122B-4A7E-A35A-0E1001016D98"
	key, err := parsePermanentKey(uppercase)
	if err != nil {
		t.Fatalf("parsePermanentKey(%q): %v", uppercase, err)
	}
	if got, want := key.String(), strings.ToLower(uppercase); got != want {
		t.Fatalf("key.String() = %q, want canonical lowercase %q", got, want)
	}
}

func TestParseStorageKeysRejectsWrongNamespaceAndMalformedUUIDs(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"permanent/d9428888-122b-4a7e-a35a-0e1001016d98",
		"staging/../d9428888-122b-4a7e-a35a-0e1001016d98",
		"staging/d9428888-122b-9a7e-a35a-0e1001016d98",
		"staging/d9428888-122b-4a7e-735a-0e1001016d98",
		"staging/00000000-0000-0000-0000-000000000000",
		"staging/{d9428888-122b-4a7e-a35a-0e1001016d98}",
		"staging/d9428888122b4a7ea35a0e1001016d98",
	} {
		if _, err := parseStagingKey(value); !errors.Is(err, errInvalidStorageKey) {
			t.Errorf("parseStagingKey(%q) error = %v, want invalid key", value, err)
		}
	}

	if _, err := parsePermanentKey("staging/d9428888-122b-4a7e-a35a-0e1001016d98"); !errors.Is(err, errInvalidStorageKey) {
		t.Errorf("parsePermanentKey with staging namespace error = %v, want invalid key", err)
	}
}

func TestGeneratedStorageKeysAreDistinctAndValid(t *testing.T) {
	t.Parallel()

	staging, err := newStagingKey()
	if err != nil {
		t.Fatal(err)
	}
	permanent, err := newPermanentKey()
	if err != nil {
		t.Fatal(err)
	}
	if !staging.valid() || !permanent.valid() {
		t.Fatal("generated key is invalid")
	}
	if staging.id == permanent.id {
		t.Fatal("generated staging and permanent IDs unexpectedly match")
	}
	if staging.id.Version() != uuid.Version(4) || permanent.id.Version() != uuid.Version(4) {
		t.Fatalf("generated key versions = %d and %d, want UUIDv4", staging.id.Version(), permanent.id.Version())
	}
}

func TestZeroStorageKeysHaveNoUsablePath(t *testing.T) {
	t.Parallel()

	if (stagingKey{}).valid() || (stagingKey{}).String() != "" {
		t.Fatal("zero staging key must be invalid and have no path")
	}
	if (permanentKey{}).valid() || (permanentKey{}).String() != "" {
		t.Fatal("zero permanent key must be invalid and have no path")
	}
}
