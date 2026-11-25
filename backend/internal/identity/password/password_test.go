package password

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type betterAuthFixture struct {
	Password               string `json:"password"`
	EquivalentNFKCPassword string `json:"equivalentNfkcPassword"`
	Hash                   string `json:"hash"`
}

func TestVerifyBetterAuthFixtureAndNFKCEquivalent(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "..", ".scratch", "phase-07-go-backend-migration", "fixtures", "better-auth-scrypt-v1.json")
	fixtureData, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read Better Auth fixture: %v", err)
	}
	var fixture betterAuthFixture
	if err := json.Unmarshal(fixtureData, &fixture); err != nil {
		t.Fatalf("decode Better Auth fixture: %v", err)
	}

	for _, candidate := range []string{fixture.Password, fixture.EquivalentNFKCPassword} {
		matched, err := Verify(candidate, fixture.Hash)
		if err != nil {
			t.Fatalf("verify fixture password: %v", err)
		}
		if !matched {
			t.Fatal("expected Better Auth fixture password to match")
		}
	}
	matched, err := Verify("wrong password", fixture.Hash)
	if err != nil {
		t.Fatalf("verify wrong password: %v", err)
	}
	if matched {
		t.Fatal("wrong password matched Better Auth fixture")
	}
}

func TestHashCreatesBetterAuthCompatibleHash(t *testing.T) {
	encoded, err := Hash("compatible password")
	if err != nil {
		t.Fatalf("Hash returned an error: %v", err)
	}
	matched, err := Verify("compatible password", encoded)
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if !matched {
		t.Fatal("generated hash did not match its password")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, encoded := range []string{"", "salt:hash", "00000000000000000000000000000000:zz"} {
		if _, err := Verify("password", encoded); err == nil {
			t.Errorf("Verify(%q) returned nil error", encoded)
		}
	}
}
