// Package secrets seals store credentials at rest.
//
// OAuth tokens for GOG and Epic live in `store_connections` and must never be stored
// as plaintext. Values are encrypted with AES-256-GCM under the deployment key from
// GAMEDEPOT_CREDENTIAL_KEY and written as `enc:v1:<iv>:<tag>:<ciphertext>`, each part
// standard base64. The version prefix is kept so a future key rotation can tell which
// format a row was written in.
//
// Both directions fail closed: sealing without a key is an error rather than a silent
// plaintext write, and opening a value that is not sealed is an error rather than a
// pass-through. This schema has no unencrypted history, so a bare token in the column
// means someone wrote it by hand.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const sealPrefix = "enc:v1:"

// KeyLen is the required deployment key size in bytes.
const KeyLen = 32

// Box seals and opens credential values with one key.
type Box struct {
	key []byte
}

// ErrNoKey means the deployment key is unset, so no credential can be stored or read.
var ErrNoKey = errors.New("secrets: no credential key configured (set GAMEDEPOT_CREDENTIAL_KEY)")

// ParseKey accepts 32 bytes as hex (64 characters, any case) or as standard base64
// (`openssl rand -hex 32` or `openssl rand -base64 32`). Anything else is rejected
// rather than padded or hashed, so a truncated key cannot quietly become a weaker one.
func ParseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrNoKey
	}
	if len(raw) == 64 && isHex(raw) {
		key, err := hex.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("secrets: credential key is not valid hex: %w", err)
		}
		return key, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != KeyLen {
		return nil, errors.New("secrets: credential key must be 32 bytes as hex or standard base64 (openssl rand -hex 32)")
	}
	// Reject alternative alphabets and loose padding by round-tripping.
	if base64.StdEncoding.EncodeToString(key) != raw {
		return nil, errors.New("secrets: credential key must be 32 bytes as hex or standard base64 (openssl rand -hex 32)")
	}
	return key, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// NewBox builds a sealing box from a raw key string. Use NewBoxFromBytes when the key
// is already parsed.
func NewBox(rawKey string) (*Box, error) {
	key, err := ParseKey(rawKey)
	if err != nil {
		return nil, err
	}
	return &Box{key: key}, nil
}

// Enabled reports whether a usable key was supplied. Store routes refuse to run without
// one; the rest of the server does not care.
func (b *Box) Enabled() bool { return b != nil && len(b.key) == KeyLen }

// Seal encrypts one credential value. The empty string stays empty so an optional
// field (a refresh token a provider did not issue) does not need a special case upstream.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if !b.Enabled() {
		return "", ErrNoKey
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", fmt.Errorf("secrets: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secrets: gcm: %w", err)
	}
	iv := make([]byte, gcm.NonceSize()) // 12 bytes, fresh per call
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	sealed := gcm.Seal(nil, iv, []byte(plain), nil)
	tagSize := gcm.Overhead()
	ivB64 := base64.StdEncoding.EncodeToString(iv)
	tagB64 := base64.StdEncoding.EncodeToString(sealed[len(sealed)-tagSize:])
	ctB64 := base64.StdEncoding.EncodeToString(sealed[:len(sealed)-tagSize])
	return sealPrefix + ivB64 + ":" + tagB64 + ":" + ctB64, nil
}

// Open decrypts a value written by Seal.
func (b *Box) Open(sealedValue string) (string, error) {
	if sealedValue == "" {
		return "", nil
	}
	if !IsSealed(sealedValue) {
		return "", errors.New("secrets: stored credential is not sealed; refusing to read it as plaintext")
	}
	if !b.Enabled() {
		return "", ErrNoKey
	}
	parts := strings.Split(strings.TrimPrefix(sealedValue, sealPrefix), ":")
	if len(parts) != 3 {
		return "", errors.New("secrets: malformed sealed credential")
	}
	var raw [3][]byte
	for i, p := range parts {
		buf, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			return "", fmt.Errorf("secrets: malformed sealed credential: %w", err)
		}
		raw[i] = buf
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", fmt.Errorf("secrets: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secrets: gcm: %w", err)
	}
	if len(raw[0]) != gcm.NonceSize() || len(raw[1]) != gcm.Overhead() {
		return "", errors.New("secrets: malformed sealed credential")
	}
	plain, err := gcm.Open(nil, raw[0], append(append([]byte{}, raw[2]...), raw[1]...), nil)
	if err != nil {
		return "", errors.New("secrets: credential could not be decrypted - the key changed or the value is corrupt")
	}
	return string(plain), nil
}

// IsSealed reports whether a stored value carries the seal prefix.
func IsSealed(value string) bool { return strings.HasPrefix(value, sealPrefix) }
