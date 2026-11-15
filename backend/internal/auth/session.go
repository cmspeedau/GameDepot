package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

const SessionCookie = "gamedepot_session"
const SessionLifetime = 30 * 24 * time.Hour
const RememberLifetime = 90 * 24 * time.Hour

type SessionManager struct {
	Pool *pgxpool.Pool
	Q    *db.Queries
}

func NewSessionManager(pool *pgxpool.Pool) *SessionManager {
	return &SessionManager{Pool: pool, Q: db.New(pool)}
}

func NewToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func NewCSRFSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func SetSessionCookie(w http.ResponseWriter, token string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1})
}

func SecureCookie(externalURL string) bool {
	return strings.HasPrefix(externalURL, "https://")
}

type Session struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	CSRF      []byte
	ExpiresAt time.Time
}

func (m *SessionManager) Create(ctx context.Context, userID uuid.UUID, remember bool, ua string) (string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	csrf, err := NewCSRFSecret()
	if err != nil {
		return "", err
	}
	lifetime := SessionLifetime
	if remember {
		lifetime = RememberLifetime
	}
	expires := time.Now().Add(lifetime)
	_, err = m.Q.CreateSession(ctx, db.CreateSessionParams{
		ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
		UserID:     pgtype.UUID{Bytes: userID, Valid: true},
		TokenHash:  hash,
		CsrfSecret: csrf,
		UserAgent:  ua,
		Remember:   remember,
		ExpiresAt:  pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (m *SessionManager) Get(ctx context.Context, token string) (*Session, error) {
	row, err := m.Q.GetSessionByTokenHash(ctx, TokenHash(token))
	if err != nil {
		return nil, err
	}
	s := &Session{
		CSRF:      row.CsrfSecret,
		ExpiresAt: row.ExpiresAt.Time,
	}
	if row.ID.Valid {
		s.ID = row.ID.Bytes
	}
	if row.UserID.Valid {
		s.UserID = row.UserID.Bytes
	}
	return s, nil
}

func (m *SessionManager) Delete(ctx context.Context, token string) error {
	row, err := m.Q.GetSessionByTokenHash(ctx, TokenHash(token))
	if err != nil {
		return err
	}
	if !row.ID.Valid {
		return fmt.Errorf("invalid session id")
	}
	return m.Q.DeleteSession(ctx, row.ID)
}

func CSRFMatch(session *Session, submitted string) bool {
	// constant-time compare of hex-encoded submitted vs stored
	if session == nil {
		return false
	}
	decoded, err := base64.RawStdEncoding.DecodeString(submitted)
	if err != nil {
		return false
	}
	if len(decoded) != len(session.CSRF) {
		return false
	}
	var diff byte
	for i := range decoded {
		diff |= decoded[i] ^ session.CSRF[i]
	}
	return diff == 0
}
