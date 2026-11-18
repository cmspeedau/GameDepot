package gog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/cmspeedau/gamedepot/internal/store"
)

// stubGog serves the three documented account endpoints from one server and records
// the paths it was asked for, mirroring the fixture harness the legacy suite used.
type stubGog struct {
	server *httptest.Server
	calls  []string
	auth   string // expected Authorization header
	owned  any
	// products returns the page body for a page number.
	products func(page int) any
	details  func(id string) any
}

func newStubGog(t *testing.T) *stubGog {
	t.Helper()
	s := &stubGog{auth: "Bearer test-token"}
	mux := http.NewServeMux()
	mux.HandleFunc("/user/data/games", func(w http.ResponseWriter, r *http.Request) {
		s.calls = append(s.calls, r.URL.Path)
		if !s.checkAuth(w, r) {
			return
		}
		writeJSON(w, s.ownedResponse())
	})
	mux.HandleFunc("/account/getFilteredProducts", func(w http.ResponseWriter, r *http.Request) {
		s.calls = append(s.calls, r.URL.Path+"?"+r.URL.RawQuery)
		if !s.checkAuth(w, r) {
			return
		}
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if s.products == nil {
			writeJSON(w, map[string]any{"totalPages": 1, "products": []any{}})
			return
		}
		writeJSON(w, s.products(p))
	})
	mux.HandleFunc("/account/gameDetails/", func(w http.ResponseWriter, r *http.Request) {
		s.calls = append(s.calls, r.URL.Path)
		if !s.checkAuth(w, r) {
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/account/gameDetails/"), ".json")
		if s.details == nil {
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, s.details(id))
	})
	srv := httptest.NewServer(mux)
	s.server = srv
	t.Cleanup(srv.Close)
	return s
}

func (s *stubGog) ownedResponse() any {
	if s.owned == nil {
		return map[string]any{"owned": []any{}}
	}
	return s.owned
}

func (s *stubGog) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != s.auth {
		w.WriteHeader(http.StatusFound) // what GOG does with a dead token
		return false
	}
	return true
}

func (s *stubGog) client() *Client {
	return NewClient(Config{
		AuthURL:          s.server.URL,
		EmbedURL:         s.server.URL,
		ContentSystemURL: s.server.URL,
		CDNURL:           s.server.URL,
		HTTPClient:       s.server.Client(),
	}, Tokens{AccessToken: "test-token", RefreshToken: "test-refresh"})
}

func (s *stubGog) detailCalls() []string {
	var out []string
	for _, c := range s.calls {
		if strings.HasPrefix(c, "/account/gameDetails/") {
			out = append(out, c)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestAuthorizeURLUsesTheRegisteredClientAndRedirect(t *testing.T) {
	u, err := url.Parse(AuthorizeURL())
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://auth.gog.com/auth" {
		t.Fatalf("authorize endpoint = %q", got)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("layout") != "galaxy" {
		t.Fatalf("authorize params = %q", q.Encode())
	}
	// Any other client/redirect combination gets GOG's "invalid client" page.
	if q.Get("client_id") != publicClientID {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "https://embed.gog.com/on_login_success?origin=client" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestExtractAuthCode(t *testing.T) {
	cases := map[string]string{
		"https://embed.gog.com/on_login_success?origin=client&code=abc-123": "abc-123",
		"  abc-123  ": "abc-123",
		// A URL without a code parameter is treated as a bare code, not dropped.
		"https://embed.gog.com/on_login_success": "https://embed.gog.com/on_login_success",
	}
	for in, want := range cases {
		if got := ExtractAuthCode(in); got != want {
			t.Errorf("ExtractAuthCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidProductID(t *testing.T) {
	good := []string{"1711765822", "1", "0"}
	bad := []string{"", "  ", "junk", "12;rm -rf", "../etc", "1e9", "-1", strings.Repeat("1", 21)}
	for _, v := range good {
		if !ValidProductID(v) {
			t.Errorf("ValidProductID(%q) = false, want true", v)
		}
	}
	for _, v := range bad {
		if ValidProductID(v) {
			t.Errorf("ValidProductID(%q) = true, want false", v)
		}
	}
}

func TestExchangeCodeSendsTheCodeToTheTokenEndpoint(t *testing.T) {
	var seenURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURL = r.URL.String()
		writeJSON(w, map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600, "user_id": "999",
		})
	}))
	defer srv.Close()

	c := NewClient(Config{AuthURL: srv.URL, HTTPClient: srv.Client()}, Tokens{})
	tokens, err := c.ExchangeCode(context.Background(),
		"https://embed.gog.com/on_login_success?origin=client&code=the-code")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccountID != "999" || tokens.AccessToken != "at" || tokens.RefreshToken != "rt" {
		t.Fatalf("tokens = %+v", tokens)
	}
	u, err := url.Parse(seenURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", q.Get("grant_type"))
	}
	if q.Get("code") != "the-code" {
		t.Errorf("code = %q, want the pasted code", q.Get("code"))
	}
	if q.Get("redirect_uri") != embedRedirectURI {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if !strings.HasPrefix(seenURL, "/token?") {
		t.Errorf("token endpoint path = %q", seenURL)
	}
}

func TestTokenErrorPassesThroughGOGsExplanation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "invalid_grant", "error_description": "The authorization code is invalid"})
	}))
	defer srv.Close()

	c := NewClient(Config{AuthURL: srv.URL, HTTPClient: srv.Client()}, Tokens{})
	_, err := c.ExchangeCode(context.Background(), "used-code")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "The authorization code is invalid") {
		t.Fatalf("error lost GOG's explanation: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error lost the status: %v", err)
	}
}

func TestExchangeCodeRejectsEmptyInput(t *testing.T) {
	c := NewClient(Config{AuthURL: "https://auth.invalid"}, Tokens{})
	var cfgErr *store.ConfigError
	_, err := c.ExchangeCode(context.Background(), "   ")
	if err == nil {
		t.Fatal("want a config error for empty input")
	}
	if !errors.As(err, &cfgErr) {
		t.Fatalf("want *store.ConfigError, got %T: %v", err, err)
	}
}

func TestRefreshSendsRefreshToken(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Encode()
		writeJSON(w, map[string]any{"access_token": "at2", "refresh_token": "rt2", "expires_in": 3600})
	}))
	defer srv.Close()

	c := NewClient(Config{AuthURL: srv.URL, HTTPClient: srv.Client()}, Tokens{RefreshToken: "rt"})
	tokens, err := c.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tokens.RefreshToken != "rt2" {
		t.Fatal("the rotated refresh token must come back to the caller for storage")
	}
	if !strings.Contains(seen, "grant_type=refresh_token") || !strings.Contains(seen, "refresh_token=rt") {
		t.Fatalf("refresh request = %q", seen)
	}
}

func TestRefreshWithoutTokenIsAConfigError(t *testing.T) {
	c := NewClient(Config{AuthURL: "https://auth.invalid"}, Tokens{})
	if _, err := c.Refresh(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}

func TestLibraryUsesDocumentedEndpoints(t *testing.T) {
	s := newStubGog(t)
	s.owned = map[string]any{"owned": []any{1711765822}}
	s.products = func(int) any {
		return map[string]any{
			"totalPages": 1,
			"products": []any{
				map[string]any{"id": 1711765822, "title": "Timberborn",
					"worksOn": map[string]any{"Windows": true, "Mac": true, "Linux": false}},
				map[string]any{"id": "junk", "title": "Broken entry"},
			},
		}
	}

	games, err := s.client().Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 {
		t.Fatalf("games = %+v, want only the valid id", games)
	}
	if games[0].ID != "1711765822" || games[0].Title != "Timberborn" {
		t.Fatalf("game = %+v", games[0])
	}
	if strings.Join(games[0].Platforms, ",") != "mac,windows" &&
		strings.Join(games[0].Platforms, ",") != "windows,mac" {
		t.Fatalf("platforms = %v, want windows and mac only", games[0].Platforms)
	}
	var sawProducts, sawOwned bool
	for _, c := range s.calls {
		switch {
		case strings.HasPrefix(c, "/account/getFilteredProducts"):
			sawProducts = true
			if !strings.Contains(c, "mediaType=1") {
				t.Errorf("filtered products must send the documented mediaType=1: %q", c)
			}
		case c == "/user/data/games":
			sawOwned = true
		}
	}
	if !sawProducts || !sawOwned {
		t.Fatalf("endpoints called = %v", s.calls)
	}
}

func TestRedirectFromAccountEndpointMeansReauth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.gog.com/login", http.StatusFound)
	}))
	defer srv.Close()

	c := NewClient(Config{EmbedURL: srv.URL, HTTPClient: srv.Client()}, Tokens{AccessToken: "expired"})
	_, err := c.Library(context.Background())
	if err == nil {
		t.Fatal("a login redirect must fail, not parse as a page of HTML")
	}
	if !errors.Is(err, store.ErrReauth) {
		t.Fatalf("err = %v, want store.ErrReauth", err)
	}
}

func TestLibraryFollowsPages(t *testing.T) {
	s := newStubGog(t)
	s.owned = map[string]any{"owned": []any{}}
	s.products = func(page int) any {
		return map[string]any{
			"totalPages": 2,
			"products":   []any{map[string]any{"id": 100 + page, "title": fmt.Sprintf("Game %d", page)}},
		}
	}
	games, err := s.client().Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 || games[0].Title != "Game 1" || games[1].Title != "Game 2" {
		t.Fatalf("games = %+v", games)
	}
	pages := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, "/account/getFilteredProducts") {
			pages++
		}
	}
	if pages != 2 {
		t.Fatalf("fetched %d pages, want 2", pages)
	}
}

func TestHiddenOwnedProductsComeBackThroughDetails(t *testing.T) {
	s := newStubGog(t)
	s.owned = map[string]any{"owned": []any{201, 202, 203}}
	s.products = func(int) any {
		return map[string]any{"totalPages": 1, "products": []any{
			map[string]any{"id": 201, "title": "Listed Game"},
		}}
	}
	s.details = func(id string) any {
		switch id {
		case "202": // a real game the filtered view hid
			return map[string]any{
				"title":     "Hidden Gem",
				"downloads": []any{[]any{"English", map[string]any{"windows": []any{map[string]any{"name": "installer"}}}}},
			}
		default: // 203: DLC, bundle SKU, extra or delisted - GOG's own verdict is []
			return []any{}
		}
	}

	games, err := s.client().Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 || games[1].ID != "202" || games[1].Title != "Hidden Gem" {
		t.Fatalf("games = %+v", games)
	}
	details := s.detailCalls()
	want := []string{"/account/gameDetails/202.json", "/account/gameDetails/203.json"}
	if strings.Join(details, " ") != strings.Join(want, " ") {
		t.Fatalf("details calls = %v, want %v (only ids missing from the list)", details, want)
	}
}

func TestDetailsTopUpFailureDoesNotLoseTheLibrary(t *testing.T) {
	s := newStubGog(t)
	s.owned = map[string]any{"owned": []any{301}}
	s.products = func(int) any {
		return map[string]any{"totalPages": 1, "products": []any{
			map[string]any{"id": 201, "title": "Listed Game"},
		}}
	}
	s.details = func(string) any { return map[string]any{} } // malformed: no title, no downloads
	games, err := s.client().Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 {
		t.Fatalf("games = %+v, want the listed one to survive", games)
	}
}

func TestBuildsListFromContentSystem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/products/1711765822/os/windows/builds" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("generation") != "2" {
			t.Errorf("generation = %q, want 2", r.URL.Query().Get("generation"))
		}
		writeJSON(w, map[string]any{"items": []any{
			map[string]any{"build_id": "b2", "product_id": "1711765822", "os": "windows",
				"version_name": "1.5", "generation": 2, "link": "https://link.invalid/b2"},
			map[string]any{"build_id": "b1"}, // no link: not downloadable
		}})
	}))
	defer srv.Close()

	c := NewClient(Config{ContentSystemURL: srv.URL, HTTPClient: srv.Client()}, Tokens{AccessToken: "test-token"})
	builds, err := c.Builds(context.Background(), "1711765822", "windows")
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 || builds[0].BuildID != "b2" || builds[0].VersionName != "1.5" {
		t.Fatalf("builds = %+v", builds)
	}
}

func TestBuildsRejectsBadProductID(t *testing.T) {
	c := NewClient(DefaultConfig(), Tokens{AccessToken: "t"})
	if _, err := c.Builds(context.Background(), "../etc", "windows"); err == nil {
		t.Fatal("want a config error for an invalid id")
	}
}
