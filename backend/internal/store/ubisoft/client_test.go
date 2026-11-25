package ubisoft

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateUbisoftSessionSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ticket": "tkt", "sessionId": "sid", "userId": "uid", "rememberMeTicket": "rm",
		})
	}))
	defer srv.Close()
	cfg := Config{SessionsURL: srv.URL, Timeout: 5 * 1e9}
	res, err := CreateUbisoftSession(context.Background(), cfg, "a@b.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if res.Session == nil || res.Session.Ticket != "tkt" || res.Session.UserID != "uid" {
		t.Fatalf("session = %+v", res.Session)
	}
	if res.TwoFactor != nil {
		t.Fatal("unexpected 2FA")
	}
}

func TestCreateUbisoftSessionTwoFactor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"twoFactorAuthenticationTicket": "2fa-tkt",
			"twoFactorAuthenticationMethod": "email",
		})
	}))
	defer srv.Close()
	cfg := Config{SessionsURL: srv.URL, Timeout: 5 * 1e9}
	res, err := CreateUbisoftSession(context.Background(), cfg, "a@b.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if res.Session != nil {
		t.Fatal("expected 2FA, got session")
	}
	if res.TwoFactor == nil || res.TwoFactor.TwoFactorTicket != "2fa-tkt" {
		t.Fatalf("2fa = %+v", res.TwoFactor)
	}
}

func TestCreateUbisoftSessionChallengeRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Ubi-Challenge", "nonce-1")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"message": "challenge"})
			return
		}
		if r.Header.Get("Ubi-Challenge") != "nonce-1" {
			t.Errorf("challenge not echoed")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ticket": "tkt", "userId": "uid"})
	}))
	defer srv.Close()
	cfg := Config{SessionsURL: srv.URL, Timeout: 5 * 1e9}
	res, err := CreateUbisoftSession(context.Background(), cfg, "a@b.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if res.Session == nil || res.Session.Ticket != "tkt" {
		t.Fatalf("session = %+v", res.Session)
	}
}

func TestGetUbisoftOwnedTitles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"viewer": map[string]any{
					"games": map[string]any{
						"nodes": []map[string]any{
							{"id": "1", "spaceId": "sp-1", "name": "Game One"},
							{"id": "2", "spaceId": "sp-2", "name": "Game Two"},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()
	cfg := Config{GraphQLURL: srv.URL, Timeout: 5 * 1e9}
	titles, err := GetUbisoftOwnedTitles(context.Background(), cfg, "tkt")
	if err != nil {
		t.Fatal(err)
	}
	if len(titles) != 2 || titles[0].SpaceID != "sp-1" || titles[1].Name != "Game Two" {
		t.Fatalf("titles = %+v", titles)
	}
}

func TestGetUbisoftOwnedTitlesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "boom", "extensions": map[string]any{"code": "X"}}},
		})
	}))
	defer srv.Close()
	cfg := Config{GraphQLURL: srv.URL, Timeout: 5 * 1e9}
	_, err := GetUbisoftOwnedTitles(context.Background(), cfg, "tkt")
	if err == nil || !contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientLibrary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"viewer": map[string]any{
					"games": map[string]any{
						"nodes": []map[string]any{
							{"id": "1", "spaceId": "sp-1", "name": "Game One"},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()
	c := NewClient(Config{GraphQLURL: srv.URL, Timeout: 5 * 1e9}, Tokens{Ticket: "tkt"})
	games, err := c.Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].ID != "sp-1" || games[0].Title != "Game One" {
		t.Fatalf("games = %+v", games)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
