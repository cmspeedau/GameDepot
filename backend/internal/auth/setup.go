package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

type setupTokenRecord struct {
	Hash string `json:"hash"`
}

// EnsureSetupToken creates a one-time setup token when the instance is unconfigured
// and returns it for logging. It returns an empty string when setup is complete.
func EnsureSetupToken(ctx context.Context, pool *pgxpool.Pool, externalURL string) (string, error) {
	q := db.New(pool)

	count, err := q.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if count > 0 {
		return "", nil
	}

	if s, err := q.GetSetting(ctx, "setup_complete"); err == nil && len(s.Value) > 0 {
		return "", nil
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	rec := setupTokenRecord{Hash: base64.RawURLEncoding.EncodeToString(TokenHash(token))}
	raw, _ := json.Marshal(rec)
	if err := q.SetSetting(ctx, db.SetSettingParams{Key: "setup_token", Value: raw}); err != nil {
		return "", err
	}

	_ = externalURL
	return token, nil
}

// VerifySetupToken checks a submitted token against the stored hash.
func VerifySetupToken(stored []byte, token string) bool {
	var rec setupTokenRecord
	if err := json.Unmarshal(stored, &rec); err != nil {
		return false
	}
	expected, err := base64.RawURLEncoding.DecodeString(rec.Hash)
	if err != nil {
		return false
	}
	actual := TokenHash(token)
	if len(expected) != len(actual) {
		return false
	}
	var diff byte
	for i := range expected {
		diff |= expected[i] ^ actual[i]
	}
	return diff == 0 && subtle.ConstantTimeCompare(expected, actual) == 1
}

// CompleteSetup marks setup as done and removes the token.
func CompleteSetup(ctx context.Context, pool *pgxpool.Pool) error {
	q := db.New(pool)
	if err := q.DeleteSetting(ctx, "setup_token"); err != nil {
		return err
	}
	return q.SetSetting(ctx, db.SetSettingParams{Key: "setup_complete", Value: []byte(`true`)})
}

func SetupTokenStored(ctx context.Context, pool *pgxpool.Pool) ([]byte, error) {
	q := db.New(pool)
	s, err := q.GetSetting(ctx, "setup_token")
	if err != nil {
		return nil, err
	}
	return s.Value, nil
}
