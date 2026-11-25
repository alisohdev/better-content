package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/scrypt"
	"golang.org/x/text/unicode/norm"
)

const (
	saltBytes = 16
	keyBytes  = 64
	scryptN   = 1 << 14
	scryptR   = 16
	scryptP   = 1
)

var errInvalidHash = errors.New("invalid password hash")

func Hash(value string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("could not generate password salt")
	}
	saltHex := hex.EncodeToString(salt)
	derived, err := derive(value, []byte(saltHex))
	if err != nil {
		return "", errors.New("could not derive password hash")
	}
	return saltHex + ":" + hex.EncodeToString(derived), nil
}

func Verify(value, encoded string) (bool, error) {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 || len(parts[0]) != saltBytes*2 || len(parts[1]) != keyBytes*2 {
		return false, errInvalidHash
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return false, errInvalidHash
	}
	expected, err := hex.DecodeString(parts[1])
	if err != nil {
		return false, errInvalidHash
	}
	actual, err := derive(value, []byte(parts[0]))
	if err != nil {
		return false, errors.New("could not verify password")
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func derive(value string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(norm.NFKC.String(value)), salt, scryptN, scryptR, scryptP, keyBytes)
}
