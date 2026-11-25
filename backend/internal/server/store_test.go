package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cmspeedau/gamedepot/internal/config"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/secrets"
	"github.com/cmspeedau/gamedepot/internal/store"
)

func secretsTestKey() string {
	// A fixed 32-byte key; tests here only need the box to be usable, not secret.
	return strings.Repeat("ab", 32)
}

func testConfig() config.Config {
	return config.Config{
		DatabaseURL: "postgres://x/y",
		ExternalURL: "http://localhost:4000",
		Port:        "4000",
		LogLevel:    "error",
	}
}

// TestStoreTemplatesRender keeps the store pages honest: a template typo would only
// surface as a blank dashboard otherwise, and ADR-0003 requires pages to be rendered in
// tests because html/template is not type checked.
func TestStoreTemplatesRender(t *testing.T) {
	s := New(testConfig(), nil)

	games := []store.Game{
		{ID: "1711765822", Title: "Timberborn", Platforms: []string{"macos", "windows"}},
		{ID: "1207658695", Title: "Hades & the Escape", Platforms: []string{"linux", "windows"}},
		{ID: "100", Title: "Windows Only", Platforms: nil},
	}
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, "stores_library.html", map[string]any{
		"Games": games,
		"CSRF":  "token-abc",
	}); err != nil {
		t.Fatalf("stores_library.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Timberborn",
		"Hades &amp; the Escape", // escaping is the template's job, not the caller's
		"value=\"1711765822\"",
		"/dashboard/stores/gog/ingest",
		"name=\"_csrf\" value=\"token-abc\"",
		`value="macos"`, `value="windows"`, // each title offers only what GOG reports
		`<option value="linux" disabled>`, // Linux is visible but not selectable (ADR-0010)
		"no Linux depot builds",           // and the fragment explains why
		"name=\"force\"",                  // and the re-download override the runbook describes
	} {
		if !strings.Contains(out, want) {
			t.Errorf("library fragment missing %q", want)
		}
	}

	// A title GOG reports for Windows only must not be offered platforms it lacks.
	windowsOnly := out[strings.Index(out, "Windows Only"):]
	if strings.Contains(windowsOnly, `value="macos"`) || strings.Contains(windowsOnly, `value="linux"`) {
		t.Error("a windows-only title offered platforms GOG does not report for it")
	}

	// The empty and error branches.
	buf.Reset()
	if err := s.templates.ExecuteTemplate(&buf, "stores_library.html", map[string]any{
		"Error": "No GOG connection. Connect an account first.",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No GOG connection") {
		t.Fatalf("error branch missing: %q", buf.String())
	}

	buf.Reset()
	if err := s.templates.ExecuteTemplate(&buf, "stores_library.html", map[string]any{
		"Games": []store.Game{},
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No entitled games") {
		t.Fatalf("empty branch missing: %q", buf.String())
	}
}

func TestStoresPageRendersWithConnections(t *testing.T) {
	s := New(testConfig(), nil)
	id := uuid.New()
	rows := []storeView{{
		ID: id.String(), Provider: "gog", Account: "player@example",
		Status: "active", ExpiresAt: "2026-11-18 21:30", UpdatedAt: "2026-11-18 09:30",
	}}
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, "stores.html", map[string]any{
		"Title":            "Store connections",
		"Connections":      rows,
		"AuthorizeURL":     "https://auth.gog.com/auth?client_id=1",
		"EpicAuthorizeURL": "https://www.epicgames.com/id/login?redirectUrl=x",
		"CSRF":             "tok",
	}); err != nil {
		t.Fatalf("stores.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"player@example",
		"active",
		"/dashboard/stores/connection/" + id.String() + "/disconnect",
		"https://auth.gog.com/auth?client_id=1",
		"on_login_success",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stores page missing %q", want)
		}
	}

	// A connection flagged for re-auth says so, in amber rather than as a success.
	buf.Reset()
	rows[0].NeedsReauth = true
	rows[0].Status = "error"
	if err := s.templates.ExecuteTemplate(&buf, "stores.html", map[string]any{
		"Connections": rows, "AuthorizeURL": "https://auth.gog.com/auth",
		"EpicAuthorizeURL": "https://www.epicgames.com/id/login",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "needs sign-in") {
		t.Fatal("a flagged connection must show it needs signing in")
	}
}

func TestMaskConnectionNeverLeaksTokens(t *testing.T) {
	row := db.StoreConnection{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Provider:     "gog",
		AccountLabel: pgtype.Text{String: "GOG account", Valid: true},
		AccountID:    pgtype.Text{String: "990022", Valid: true},
		AccessToken:  "enc:v1:aaa:bbb:ccc",
		RefreshToken: pgtype.Text{String: "enc:v1:ddd:eee:fff", Valid: true},
		ExpiresAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Status:       "active",
		UpdatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	view := maskConnection(row)
	if view.Account != "GOG account" {
		t.Fatalf("account = %q", view.Account)
	}
	if strings.Contains(view.Account, "enc:v1") {
		t.Fatal("a sealed value reached the view")
	}
	if view.NeedsReauth {
		t.Fatal("an active connection must not ask for re-auth")
	}
}

func TestEpicLibraryFragmentRenders(t *testing.T) {
	s := New(testConfig(), nil)
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, "stores_epic.html", map[string]any{
		"Games": []store.Game{
			{ID: "ns-one/item-hash", Title: "Some Racing Game"},
			{ID: "ns-two/other", Title: "Platformer"},
		},
	}); err != nil {
		t.Fatalf("stores_epic.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Some Racing Game", "ns-one/item-hash", "2 product(s)", "Epic builds are acquired"} {
		if !strings.Contains(out, want) {
			t.Errorf("epic fragment missing %q", want)
		}
	}
	// Ingestion is not offered: no form and no button, so nobody can start a download
	// that cannot finish.
	for _, forbidden := range []string{"<form", "<button", "/stores/epic/ingest"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the Epic fragment must not offer ingestion controls, found %q", forbidden)
		}
	}

	buf.Reset()
	if err := s.templates.ExecuteTemplate(&buf, "stores_epic.html", map[string]any{
		"Error": "No Epic connection. Connect an account first.",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No Epic connection") {
		t.Fatal("the error branch must render")
	}
}

func TestStoresReady(t *testing.T) {
	box, err := secrets.NewBox(secretsTestKey())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		enabled bool
		box     *secrets.Box
		want    bool
	}{
		{"off and no key", false, nil, false},
		{"on but no key", true, nil, false},
		{"off with a key", false, box, false},
		{"on with a key", true, box, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: config.Config{StoreEnabled: tc.enabled}, secrets: tc.box}
			if got := s.storesReady(); got != tc.want {
				t.Fatalf("storesReady() = %v, want %v", got, tc.want)
			}
		})
	}
}
