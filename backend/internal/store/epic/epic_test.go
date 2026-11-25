package epic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmspeedau/gamedepot/internal/store"
)

func TestAuthorizeURLWrapsTheRedirectAPI(t *testing.T) {
	raw := AuthorizeURL()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://www.epicgames.com/id/login" {
		t.Fatalf("login endpoint = %q", got)
	}
	target, err := url.Parse(u.Query().Get("redirectUrl"))
	if err != nil {
		t.Fatalf("redirectUrl is not a URL: %v", err)
	}
	if got := target.Scheme + "://" + target.Host + target.Path; got != "https://www.epicgames.com/id/api/redirect" {
		t.Fatalf("redirect target = %q", got)
	}
	if target.Query().Get("clientId") != clientID || target.Query().Get("responseType") != "code" {
		t.Fatalf("redirect params = %q", target.RawQuery)
	}
}

func TestExtractAuthCodeShapes(t *testing.T) {
	t.Run("bare code", func(t *testing.T) {
		got, err := ExtractAuthCode("  xyz-789 ")
		if err != nil || got != "xyz-789" {
			t.Fatalf("%q, %v", got, err)
		}
	})
	t.Run("redirect url", func(t *testing.T) {
		got, err := ExtractAuthCode("https://www.epicgames.com/id/api/redirect?code=abc-123&state=x")
		if err != nil || got != "abc-123" {
			t.Fatalf("%q, %v", got, err)
		}
	})
	t.Run("json body", func(t *testing.T) {
		got, err := ExtractAuthCode(`{"code":"from-json","state":""}`)
		if err != nil || got != "from-json" {
			t.Fatalf("%q, %v", got, err)
		}
	})
	t.Run("json body reporting an error", func(t *testing.T) {
		_, err := ExtractAuthCode(`{"error":"invalid_request","correlationId":"c-1"}`)
		if err == nil {
			t.Fatal("want an error rather than sending junk to Epic as a code")
		}
		var cfgErr *store.ConfigError
		if !errors.As(err, &cfgErr) {
			t.Fatalf("err = %T %v", err, err)
		}
		if !strings.Contains(err.Error(), "c-1") {
			t.Fatalf("the correlation id should survive: %v", err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if _, err := ExtractAuthCode("   "); err == nil {
			t.Fatal("want an error")
		}
	})
}

// capturedRequest keeps what the test needs to assert after the handler has consumed
// the body, which a cloned request would not.
type capturedRequest struct {
	method string
	header http.Header
	form   url.Values
}

func tokenStub(t *testing.T, status int, body any, seen *capturedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.method = r.Method
			seen.header = r.Header.Clone()
			_ = r.ParseForm() // PostForm is only filled once the body is parsed
			seen.form = r.PostForm
			if seen.form == nil {
				seen.form = url.Values{}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestExchangeCodeSendsBasicAuthAndForm(t *testing.T) {
	got := &capturedRequest{}
	srv := tokenStub(t, 200, map[string]any{
		"access_token": "at", "refresh_token": "rt", "expires_in": 7200, "account_id": "acc-1",
	}, got)
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.OAuthURL = srv.URL
	tokens, err := ExchangeCode(context.Background(), cfg, `{"code":"the-code"}`)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccountID != "acc-1" || tokens.RefreshToken != "rt" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %s", got.method)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret))
	if got.header.Get("Authorization") != want {
		t.Errorf("authorization = %q", got.header.Get("Authorization"))
	}
	form := got.form
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "the-code" || form.Get("token_type") != "eg1" {
		t.Errorf("form = %q", form.Encode())
	}
}

func TestEpicErrorBodyIsPassedThrough(t *testing.T) {
	srv := tokenStub(t, http.StatusForbidden, map[string]any{
		"errorCode":        "errors.com.epicgames.account.oauth.authorization_pending",
		"errorMessage":     "The request is not ready because it is pending",
		"correctiveAction": "exchange_code",
		"continuationUrl":  "https://www.epicgames.com/id/api/device",
	}, nil)
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.OAuthURL = srv.URL
	_, err := ExchangeCode(context.Background(), cfg, "code-1")
	if err == nil {
		t.Fatal("want the Epic error")
	}
	for _, want := range []string{
		"errors.com.epicgames.account.oauth.authorization_pending",
		"exchange_code",
		"https://www.epicgames.com/id/api/device",
		"403",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lost %q: %v", want, err)
		}
	}
}

func TestRefreshRotatesAndRequiresToken(t *testing.T) {
	srv := tokenStub(t, 200, map[string]any{
		"access_token": "at2", "refresh_token": "rt2", "expires_in": 3600,
	}, nil)
	defer srv.Close()

	c := NewClient(Config{OAuthURL: srv.URL, HTTPClient: srv.Client()}, Tokens{RefreshToken: "rt"})
	tokens, err := c.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tokens.RefreshToken != "rt2" {
		t.Fatal("the rotated refresh token must reach the caller for storage")
	}

	bare := NewClient(Config{OAuthURL: srv.URL}, Tokens{})
	if _, err := bare.Refresh(context.Background()); err == nil {
		t.Fatal("a refresh without a stored token must fail")
	}
}

// libraryStub serves the library, catalog, assets, manifest and chunk services from one
// test server.
type libraryStub struct {
	server    *httptest.Server
	pages     []map[string]any // library pages, served in order
	catalog   map[string]any   // namespace -> catalog body
	asset     any              // launcher assets body
	assetStat int
	calls     []string
	auth      string
	manifest  []byte // binary build manifest served under /manifest/
	chunk     []byte // content served under /chunk/
}

func newLibraryStub(t *testing.T, s *libraryStub) {
	mux := http.NewServeMux()
	mux.HandleFunc("/library/api/public/items", func(w http.ResponseWriter, r *http.Request) {
		s.calls = append(s.calls, r.URL.Path+"?"+r.URL.RawQuery)
		if !s.checkAuth(w, r) {
			return
		}
		page := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			page = atoiSafe(c)
		}
		if page >= len(s.pages) {
			writeJSON(w, map[string]any{"records": []any{}})
			return
		}
		writeJSON(w, s.pages[page])
	})
	mux.HandleFunc("/catalog/api/shared/namespace/", func(w http.ResponseWriter, r *http.Request) {
		s.calls = append(s.calls, r.URL.Path)
		if !s.checkAuth(w, r) {
			return
		}
		ns := strings.TrimPrefix(r.URL.Path, "/catalog/api/shared/namespace/")
		ns = strings.TrimSuffix(ns, "/bulk/items")
		body, ok := s.catalog[ns]
		if !ok {
			writeJSON(w, map[string]any{"elements": []any{}})
			return
		}
		writeJSON(w, body)
	})
	mux.HandleFunc("/launcher/api/public/assets/v2/platform/", func(w http.ResponseWriter, r *http.Request) {
		s.calls = append(s.calls, r.URL.Path+"?"+r.URL.RawQuery)
		if !s.checkAuth(w, r) {
			return
		}
		if s.assetStat != 0 && s.assetStat != http.StatusOK {
			w.WriteHeader(s.assetStat)
			return
		}
		writeJSON(w, s.asset)
	})
	mux.HandleFunc("/manifest/", func(w http.ResponseWriter, r *http.Request) {
		if len(s.manifest) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(s.manifest)
	})
	mux.HandleFunc("/chunk/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(s.chunk)
	})
	srv := httptest.NewServer(mux)
	s.server = srv
	t.Cleanup(srv.Close)
}

func (s *libraryStub) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.auth != "" && r.Header.Get("Authorization") != s.auth {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *libraryStub) client() *Client {
	return NewClient(Config{
		LibraryURL: s.server.URL + "/library/api/public/items",
		CatalogURL: s.server.URL + "/catalog/api/shared/namespace",
		AssetsURL:  s.server.URL + "/launcher/api/public/assets/v2/platform",
		OAuthURL:   s.server.URL + "/token",
		HTTPClient: s.server.Client(),
	}, Tokens{AccessToken: "test-token"})
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestLibraryPagesCursorsAndFilters(t *testing.T) {
	s := &libraryStub{
		auth: "Bearer test-token",
		pages: []map[string]any{
			{"records": []any{
				map[string]any{"namespace": "ns1", "catalogItemId": "item-hash-1", "appName": "appA", "sandboxName": "Sandbox A"},
				map[string]any{"namespace": "ue", "catalogItemId": "freebie-hash", "appName": "freebie", "sandboxName": "UE 5.3"},
				map[string]any{"namespace": "ns2", "catalogItemId": "launcher", "appName": "EpicGamesLauncher"},
				map[string]any{"namespace": "ns3", "catalogItemId": "engine", "appName": "UE_5.4"},
			}, "responseMetadata": map[string]any{"nextCursor": "1"}},
			{"records": []any{
				// duplicate of page 1's first product under another platform record
				map[string]any{"namespace": "ns1", "catalogItemId": "item-hash-1", "appName": "appA", "sandboxName": "Sandbox A"},
				map[string]any{"namespace": "ns4", "catalogItemId": "fab-item", "appName": "fabApp", "sandboxName": "fab-listing-live"},
				map[string]any{"namespace": "ns5", "catalogItemId": "item-2", "appName": "appB", "sandboxName": "Second Sandbox"},
			}},
		},
		catalog: map[string]any{
			"ns1": map[string]any{"elements": []any{
				map[string]any{"id": "item-hash-1", "title": "Real Title", "appName": "appA"},
			}},
		},
	}
	newLibraryStub(t, s)

	games, err := s.client().Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("games = %+v, want the two real products (runtime, fab and duplicates dropped)", games)
	}
	byID := map[string]string{}
	for _, g := range games {
		byID[g.ID] = g.Title
	}
	// Catalog title wins over the sandbox name; the sandbox name wins when no catalog row.
	if byID["ns1/item-hash-1"] != "Real Title" {
		t.Errorf("catalog title not used: %v", byID)
	}
	if byID["ns5/item-2"] != "Second Sandbox" {
		t.Errorf("sandbox name not used as the fallback: %v", byID)
	}
	for _, bad := range []string{"ue/freebie-hash", "ns2/launcher", "ns3/engine", "ns4/fab-item"} {
		if _, ok := byID[bad]; ok {
			t.Errorf("%s must not be listed as a game", bad)
		}
	}
	// Both pages were fetched, the second by cursor.
	if len(s.calls) < 2 || !strings.Contains(strings.Join(s.calls, " "), "cursor=1") {
		t.Errorf("library calls = %v", s.calls)
	}
}

func TestLibrarySurvivesCatalogFailure(t *testing.T) {
	s := &libraryStub{
		auth: "Bearer test-token",
		pages: []map[string]any{{"records": []any{
			map[string]any{"namespace": "ns1", "catalogItemId": "i1", "appName": "appA", "sandboxName": "Sandbox A"},
		}}},
		catalog: nil, // no handler data: the bulk call answers empty elements
	}
	newLibraryStub(t, s)
	games, err := s.client().Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].Title != "Sandbox A" {
		t.Fatalf("games = %+v, want the sandbox name fallback", games)
	}
}

func TestDeadTokenSurfacesAsReauth(t *testing.T) {
	s := &libraryStub{auth: "Bearer other", pages: nil}
	newLibraryStub(t, s)
	_, err := s.client().Library(context.Background())
	if !errors.Is(err, store.ErrReauth) {
		t.Fatalf("err = %v, want store.ErrReauth", err)
	}
}

func TestAssetResolutionAndNotFound(t *testing.T) {
	s := &libraryStub{
		auth: "Bearer test-token",
		asset: map[string]any{"elements": []any{map[string]any{
			"label": "1.5", "buildVersion": "b15", "manifestLocation": "https://cdn/Manifest_15",
			"chunkBaseDurls": []string{"https://cdn.example/base"},
		}}},
	}
	newLibraryStub(t, s)

	info, err := s.client().Asset(context.Background(), "Windows", "ns1", "item1", "appA")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.BuildVersion != "b15" || info.ManifestLocation != "https://cdn/Manifest_15" {
		t.Fatalf("asset = %+v", info)
	}
	// The platform is a path segment in Epic's API.
	if !strings.Contains(s.calls[len(s.calls)-1], "/Windows?") {
		t.Errorf("asset call = %q", s.calls[len(s.calls)-1])
	}

	s.assetStat = http.StatusNotFound
	missing, err := s.client().Asset(context.Background(), "Linux", "ns1", "item1", "appA")
	if err != nil || missing != nil {
		t.Fatalf("a 404 must read as 'no build', got %+v / %v", missing, err)
	}
}

func TestLatestBuildUsesAsset(t *testing.T) {
	s := &libraryStub{
		auth:  "Bearer test-token",
		asset: map[string]any{"elements": []any{map[string]any{"label": "v9", "buildVersion": "b9"}}},
	}
	newLibraryStub(t, s)
	info, err := s.client().LatestBuild(context.Background(), "ns1/item1", "macos")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "b9" || info.VersionName != "v9" {
		t.Fatalf("info = %+v", info)
	}
	if !strings.Contains(s.calls[len(s.calls)-1], "/Mac?") {
		t.Errorf("macos must map to Epic's Mac platform: %q", s.calls[len(s.calls)-1])
	}
}

func TestAcquireFetchesDecodesAndStagesEpicBuild(t *testing.T) {
	chunkBody := []byte("epic chunk payload")
	s := &libraryStub{
		auth:  "Bearer test-token",
		asset: map[string]any{"elements": []any{map[string]any{"buildVersion": "b1", "label": "v1"}}},
	}
	newLibraryStub(t, s)
	s.asset = map[string]any{"elements": []any{map[string]any{
		"buildVersion": "4226454", "label": "v1",
		"manifestLocation":  s.server.URL + "/manifest/build.bin",
		"distributionPoint": s.server.URL + "/chunk/",
	}}}
	s.chunk = chunkBody
	m := EpicManifestMeta{
		DataVersion:   0,
		FeatureLevel:  17,
		AppName:       "Hitman",
		BuildVersion:  "4226454",
		LaunchExe:     "Retail/HITMAN.exe",
		LaunchCommand: "",
	}
	files := []EpicManifestFile{{
		Path:       "Retail/HITMAN.exe",
		Symlink:    "",
		Sha1:       hexSHA1(make([]byte, 20)),
		Flags:      epicUnixExecutable,
		ChunkParts: []EpicManifestChunkPart{{Guid: [4]uint32{0x11223344, 0x55667788, 0x99aabbcc, 0xddeeff00}, Offset: 0, Size: uint32(len(chunkBody)), FileOffset: 0}},
		FileSize:   int64(len(chunkBody)),
	}}
	s.manifest = epicManifest(m, files)

	dir := t.TempDir()
	result, err := s.client().Acquire(context.Background(), "ns1/item1", store.AcquireOptions{
		StagingDir: dir, Platform: "windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 || result.TotalSize != int64(len(chunkBody)) {
		t.Fatalf("result = %+v", result)
	}
	if result.BuildID != "4226454" || result.VersionName != "4226454" {
		t.Fatalf("result identity = %+v", result)
	}
	if len(result.Launches) != 1 || result.Launches[0].Exe != "Retail/HITMAN.exe" {
		t.Fatalf("launches = %+v", result.Launches)
	}
	got, err := os.ReadFile(filepath.Join(dir, "4226454", "Retail", "HITMAN.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(chunkBody) {
		t.Fatalf("staged file = %q, want %q", got, chunkBody)
	}
}

func TestAcquireRefusesWhenManifestCannotBeDecoded(t *testing.T) {
	s := &libraryStub{
		auth:  "Bearer test-token",
		asset: map[string]any{"elements": []any{map[string]any{"buildVersion": "b1", "manifestLocation": "https://cdn.example/Manifest_15"}}},
	}
	newLibraryStub(t, s)

	dir := t.TempDir()
	_, err := s.client().Acquire(context.Background(), "ns1/item1", store.AcquireOptions{
		StagingDir: dir, Platform: "windows",
	})
	if err == nil {
		t.Fatal("acquisition must not silently produce nothing")
	}
	if !strings.Contains(err.Error(), "https://cdn.example/Manifest_15") {
		t.Fatalf("error should name the manifest: %v", err)
	}
	// Nothing was written into staging.
	entries, _ := readDirNames(dir)
	if len(entries) != 0 {
		t.Fatalf("staging should be empty, found %v", entries)
	}
}

func TestAcquireReportsUndecodableManifestFromServer(t *testing.T) {
	s := &libraryStub{
		auth:  "Bearer test-token",
		asset: map[string]any{"elements": []any{map[string]any{"buildVersion": "b1", "manifestLocation": "http://placeholder/Manifest_15"}}},
	}
	newLibraryStub(t, s)
	s.manifest = []byte("this is not an epic manifest")
	s.asset = map[string]any{"elements": []any{map[string]any{
		"buildVersion": "b1", "manifestLocation": s.server.URL + "/manifest/build.bin",
	}}}

	dir := t.TempDir()
	_, err := s.client().Acquire(context.Background(), "ns1/item1", store.AcquireOptions{
		StagingDir: dir, Platform: "windows",
	})
	if err == nil {
		t.Fatal("a body that fails the magic check must not acquire")
	}
	if !strings.Contains(err.Error(), "magic") {
		t.Fatalf("err = %v, want the magic mismatch surfaced", err)
	}
}

func TestAcquireAndLatestBuildValidateProductID(t *testing.T) {
	c := NewClient(DefaultConfig(), Tokens{AccessToken: "t"})
	for _, bad := range []string{"", "no-slash", "/only-item", "ns-only/"} {
		if _, err := c.Acquire(context.Background(), bad, store.AcquireOptions{}); err == nil {
			t.Errorf("Acquire(%q) accepted a malformed product id", bad)
		}
		if _, err := c.LatestBuild(context.Background(), bad, "windows"); err == nil {
			t.Errorf("LatestBuild(%q) accepted a malformed product id", bad)
		}
	}
}

func TestPlatformName(t *testing.T) {
	cases := map[string]string{"windows": "Windows", "": "Windows", "linux": "Linux", "macos": "Mac", "mac": "Mac", "solaris": "Windows"}
	for in, want := range cases {
		if got := platformName(in); got != want {
			t.Errorf("platformName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsRuntimeAndFabFilter(t *testing.T) {
	runtime := [][2]string{{"EpicGamesLauncher", "ns"}, {"UE_5.4", "ns"}, {"anything", "ue"}}
	for _, c := range runtime {
		if !isRuntime(c[0], c[1]) {
			t.Errorf("isRuntime(%q, %q) = false", c[0], c[1])
		}
	}
	if isRuntime("appA", "ns1") {
		t.Error("a normal app must not be filtered as runtime")
	}
	if !fabListing.MatchString("FAB-LISTING-live") {
		t.Error("the fab filter must be case-insensitive")
	}
	if fabListing.MatchString("Timberborn") {
		t.Error("a real title must not match the fab filter")
	}
}

func TestProviderContract(t *testing.T) {
	var p store.Provider = NewClient(DefaultConfig(), Tokens{})
	if p.Name() != "epic" {
		t.Fatalf("name = %q", p.Name())
	}
	var _ store.Provider = (*Client)(nil)
}
