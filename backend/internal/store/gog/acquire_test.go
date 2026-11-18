package gog

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5" //nolint:gosec // mirrors the provider's declared digest
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmspeedau/gamedepot/internal/store"
)

// stubContent serves the whole content-system surface for one product: build list,
// build meta, depot manifests, secure links and chunk bytes. Bodies that GOG serves
// zlib-compressed are compressed here too, so the decode path is exercised.
type stubContent struct {
	server    *httptest.Server
	builds    []map[string]any
	meta      map[string]any
	depots    map[string][]map[string]any // manifest hash -> depot items
	files     map[string][]byte           // chunk key -> plaintext bytes
	endpoints []map[string]any
	// corruptChunk, when set, breaks the bytes served for that key.
	corruptChunk string
	requests     []string
}

func newStubContent(t *testing.T) *stubContent {
	s := &stubContent{
		depots: map[string][]map[string]any{},
		files:  map[string][]byte{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/products/", func(w http.ResponseWriter, r *http.Request) {
		s.requests = append(s.requests, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/builds"):
			writeJSON(w, map[string]any{"items": s.builds})
		case strings.HasSuffix(r.URL.Path, "/secure_link"):
			urls := s.endpoints
			if len(urls) == 0 {
				urls = []map[string]any{{
					"url_format": s.server.URL + "/chunks{path}",
					"parameters": map[string]string{"path": ""},
					"priority":   1,
				}}
			}
			writeJSON(w, map[string]any{"urls": urls})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/link/build-meta", func(w http.ResponseWriter, r *http.Request) {
		writeZlib(w, s.meta)
	})
	mux.HandleFunc("/content-system/v2/meta/", func(w http.ResponseWriter, r *http.Request) {
		hash := filepath.Base(r.URL.Path)
		items, ok := s.depots[hash]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeZlib(w, map[string]any{"depot": map[string]any{"items": items}})
	})
	mux.HandleFunc("/chunks/", func(w http.ResponseWriter, r *http.Request) {
		hash := filepath.Base(r.URL.Path)
		plain, ok := s.files[hash]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body := zlibBytes(plain)
		if s.corruptChunk == hash {
			body = append([]byte{}, body...)
			body[len(body)/2] ^= 0xff
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	s.server = srv
	t.Cleanup(srv.Close)
	return s
}

func (s *stubContent) client() *Client {
	return NewClient(Config{
		AuthURL:          s.server.URL,
		EmbedURL:         s.server.URL,
		ContentSystemURL: s.server.URL,
		CDNURL:           s.server.URL,
		HTTPClient:       s.server.Client(),
	}, Tokens{AccessToken: "test-token"})
}

func writeZlib(w http.ResponseWriter, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(zlibBytes(buf))
}

func zlibBytes(b []byte) []byte {
	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return out.Bytes()
}

func md5sum(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// addFile registers one depot file: chunk entries with both declared digests, and the
// plaintext the stub serves.
func (s *stubContent) addFile(manifestHash, path string, content []byte, flags ...string) map[string]any {
	hash := md5sum(content)
	// Chunk URLs are built from compressedMd5 when the manifest declares it, so the
	// stub serves the plaintext under that key as well.
	s.files[hash] = content
	s.files[md5sum(zlibBytes(content))] = content
	item := map[string]any{
		"type": "DepotFile",
		"path": path,
		"md5":  hash,
		"chunks": []map[string]any{{
			"md5":            hash,
			"compressedMd5":  md5sum(zlibBytes(content)),
			"size":           len(content),
			"compressedSize": len(zlibBytes(content)),
		}},
	}
	if len(flags) > 0 {
		item["flags"] = flags
	}
	s.depots[manifestHash] = append(s.depots[manifestHash], item)
	return item
}

func (s *stubContent) addDirectory(manifestHash, path string) {
	s.depots[manifestHash] = append(s.depots[manifestHash], map[string]any{
		"type": "DepotDirectory", "path": path,
	})
}

const product = "1711765822"

// GOG depot manifest hashes are hex; the client refuses anything else, so fixtures
// use a realistic value.
const (
	m1hash = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	dlc    = "b1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f91"
	frdep  = "c1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f92"
	x86dep = "d1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f93"
	m9hash = "e1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f94"
)

// happyProduct wires a two-file game with a launch document.
func (s *stubContent) happyProduct() {
	s.builds = []map[string]any{{
		"build_id": "b1", "product_id": product, "os": "windows", "version_name": "1.5",
		"generation": 2, "link": s.server.URL + "/link/build-meta",
	}}
	s.meta = map[string]any{
		"buildId": "b1", "baseProductId": product, "installDirectory": "Timberborn",
		"depots": []map[string]any{{"manifest": m1hash, "productId": product, "languages": []string{"*"}}},
	}
	info := map[string]any{
		"installDirectory": "Timberborn",
		"executables": []map[string]any{
			{"name": "Play", "path": "Game.exe", "type": "Game"},
			{"name": "Config", "path": "Config.exe", "type": "Utility"},
		},
	}
	infoBytes, _ := json.Marshal(info)
	s.addFile(m1hash, "goggame-"+product+".info.json", infoBytes)
	s.addFile(m1hash, `Game\Data\save.bin`, []byte("payload"))
	s.addDirectory(m1hash, "Game")
}

func TestAcquireWritesVerifiedTree(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	dest := t.TempDir()

	result, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{StagingDir: dest})
	if err != nil {
		t.Fatal(err)
	}
	if result.VersionName != "1.5" || result.BuildID != "b1" {
		t.Fatalf("result = %+v", result)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d, want the two chunked files (directories carry no bytes)", result.Files)
	}
	got, err := os.ReadFile(filepath.Join(result.Dir, "Game", "Data", "save.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("file content = %q", got)
	}
	if len(result.Launches) != 1 || result.Launches[0].Exe != "Game.exe" {
		t.Fatalf("launches = %+v, want only the Game-typed entry", result.Launches)
	}
}

func TestAcquireRejectsEscapingDepotPath(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	// A depot that walks out of the version directory must fail before anything lands.
	s.addFile(m1hash, `..\..\evil.txt`, []byte("nope"))
	s.meta["depots"] = []map[string]any{{"manifest": m1hash, "productId": product}}

	if _, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{StagingDir: t.TempDir()}); err == nil {
		t.Fatal("want an unsafe-path error")
	} else if !strings.Contains(err.Error(), "unsafe file path") {
		t.Fatalf("err = %v", err)
	}
}

func TestAcquireDetectsCorruptChunk(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	// Break the payload file's chunk; the compressed MD5 must catch it.
	content := []byte("payload")
	s.corruptChunk = md5sum(zlibBytes(content))

	_, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{StagingDir: t.TempDir()})
	if err == nil {
		t.Fatal("a corrupted chunk must fail the acquisition")
	}
	if !strings.Contains(err.Error(), "compressed MD5 mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestAcquirePrefersMasterBranch(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	// A newer Experimental build must not hijack the stable one.
	s.builds = []map[string]any{
		{"build_id": "exp", "product_id": product, "os": "windows", "version_name": "2.0-exp",
			"branch": "Experimental", "link": s.server.URL + "/link/build-meta"},
		{"build_id": "b1", "product_id": product, "os": "windows", "version_name": "1.5",
			"branch": "Master", "link": s.server.URL + "/link/build-meta"},
	}
	result, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{StagingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.BuildID != "b1" {
		t.Fatalf("build = %s, want the Master build", result.BuildID)
	}
}

func TestAcquireSkipsDLCAndWrongLanguageAnd32Bit(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	s.meta["depots"] = []map[string]any{
		{"manifest": m1hash, "productId": product, "languages": []string{"*"}},
		{"manifest": dlc, "productId": "999999"},                                  // DLC: separate entitlement
		{"manifest": frdep, "productId": product, "languages": []string{"fr-FR"}}, // not wanted
		{"manifest": x86dep, "productId": product, "osBitness": []string{"32"}},   // 32-bit only
	}
	for _, hash := range []string{dlc, frdep, x86dep} {
		s.addFile(hash, hash+".bin", []byte("x"))
	}
	result, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{StagingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d, want only the base depot's", result.Files)
	}
	for _, path := range []string{"dlc.bin", "fr.bin", "x86.bin"} {
		if _, err := os.Stat(filepath.Join(result.Dir, path)); err == nil {
			t.Fatalf("%s was downloaded; the filter let a non-base depot through", path)
		}
	}
}

func TestAcquireRequiresLaunchDocument(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	// Drop the info document from the depot.
	kept := s.depots[m1hash][:0]
	for _, item := range s.depots[m1hash] {
		if !strings.Contains(strings.ToLower(item["path"].(string)), "goggame") {
			kept = append(kept, item)
		}
	}
	s.depots[m1hash] = kept

	_, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{StagingDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "launch document") {
		t.Fatalf("err = %v, want the missing launch document error", err)
	}
}

func TestAcquireRejectsInfoWithoutGameExecutable(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	// Replace the launch document with one that declares only a utility.
	var kept []map[string]any
	for _, item := range s.depots[m1hash] {
		if !strings.Contains(strings.ToLower(item["path"].(string)), "goggame") {
			kept = append(kept, item)
		}
	}
	s.depots[m1hash] = kept
	info, _ := json.Marshal(map[string]any{
		"executables": []map[string]any{{"name": "Config", "path": "Config.exe", "type": "Utility"}},
	})
	s.addFile(m1hash, "goggame-"+product+".info.json", info)

	if _, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{StagingDir: t.TempDir()}); err == nil {
		t.Fatal("want an error when the info document declares no game executable")
	}
}

func TestParseGogInfoBothSchemas(t *testing.T) {
	t.Run("classic executables plus plateau nest", func(t *testing.T) {
		doc := map[string]any{
			"installDirectory": "Game",
			"executables": []any{
				map[string]any{"name": "Play", "path": `Start.exe`, "type": "Game"},
			},
			"plateau": map[string]any{
				"executables": []any{
					map[string]any{"name": "Editor", "path": `tools\editor.exe`, "type": "Game", "arguments": "--edit"},
				},
			},
		}
		blob, _ := json.Marshal(doc)
		dir, launches, err := ParseGogInfo(blob)
		if err != nil {
			t.Fatal(err)
		}
		if dir != "Game" {
			t.Fatalf("installDirectory = %q", dir)
		}
		if len(launches) != 2 {
			t.Fatalf("launches = %+v", launches)
		}
		if launches[1].Exe != "tools/editor.exe" {
			t.Fatalf("backslashes must normalise: %q", launches[1].Exe)
		}
		if strings.Join(launches[1].Args, ",") != "--edit" {
			t.Fatalf("string arguments = %v", launches[1].Args)
		}
	})

	t.Run("content-system playTasks", func(t *testing.T) {
		doc := map[string]any{
			"playTasks": []any{
				map[string]any{"name": "Play", "path": "Game.exe", "category": "game", "type": "FileTask",
					"arguments": []string{"-launch"}},
				map[string]any{"name": "Overlay", "path": "Overlay.exe", "category": "tool", "type": "FileTask"},
			},
		}
		blob, _ := json.Marshal(doc)
		_, launches, err := ParseGogInfo(blob)
		if err != nil {
			t.Fatal(err)
		}
		if len(launches) != 1 || launches[0].Name != "Play" || strings.Join(launches[0].Args, ",") != "-launch" {
			t.Fatalf("launches = %+v", launches)
		}
	})

	t.Run("unreadable JSON", func(t *testing.T) {
		if _, _, err := ParseGogInfo([]byte("<html>login</html>")); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestSelectDepotsDefaultsLanguage(t *testing.T) {
	meta := BuildMeta{Depots: []Depot{
		{Manifest: "a"}, // no languages: any
		{Manifest: "b", Languages: []string{"en-US", "de-DE"}}, // wanted
		{Manifest: "c", Languages: []string{"ja-JP"}},          // not wanted
	}}
	got := selectDepots(meta, "p", "")
	if len(got) != 2 || got[0].Manifest != "a" || got[1].Manifest != "b" {
		t.Fatalf("depots = %+v", got)
	}
}

func TestGalaxyPath(t *testing.T) {
	cases := map[string]string{
		"a1b2c3d4e5":       "a1/b2/a1b2c3d4e5",
		"a1/b2/a1b2c3d4e5": "a1/b2/a1b2c3d4e5", // already prefixed
	}
	for in, want := range cases {
		if got := gogGalaxyPath(in); got != want {
			t.Errorf("gogGalaxyPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildChunkURLAppendsGalaxyToPath(t *testing.T) {
	ep := SecureEndpoint{
		URLFormat:  "https://cdn.example.com{path}",
		Parameters: map[string]string{"path": "/1234567890abcdef"},
	}
	got := BuildChunkURL(ep, "a1/b2/a1b2c3")
	if want := "https://cdn.example.com/1234567890abcdef/a1/b2/a1b2c3"; got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestSecureLinksRejectsMalformedEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"urls": []any{
			map[string]any{"url_format": "https://cdn.example.com{path}", "parameters": map[string]string{"path": "/signed"}, "priority": 5},
			map[string]any{"url_format": ""},                               // no template
			map[string]any{"url_format": "https://cdn.example/{path} bad"}, // whitespace
		}})
	}))
	defer srv.Close()
	c := NewClient(Config{ContentSystemURL: srv.URL, HTTPClient: srv.Client()}, Tokens{AccessToken: "t"})
	out, err := c.SecureLinks(context.Background(), product)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("endpoints = %+v", out)
	}
}

func TestSecureLinksNoneIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"urls": []any{}})
	}))
	defer srv.Close()
	c := NewClient(Config{ContentSystemURL: srv.URL, HTTPClient: srv.Client()}, Tokens{AccessToken: "t"})
	if _, err := c.SecureLinks(context.Background(), product); err == nil {
		t.Fatal("want an error when no endpoints come back")
	}
}

func TestDepotManifestSkipsNonFilesAndNormalisesPaths(t *testing.T) {
	s := newStubContent(t)
	s.addFile(m9hash, `Sub\file.bin`, []byte("abc"))
	s.addDirectory(m9hash, "Sub")
	s.depots[m9hash] = append(s.depots[m9hash], map[string]any{"type": "DepotLink", "path": "link.txt"})

	files, err := s.client().DepotManifest(context.Background(), m9hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	if files[0].Path != "Sub/file.bin" {
		t.Fatalf("path = %q, want forward slashes", files[0].Path)
	}
	if files[0].Size != 3 {
		t.Fatalf("size = %d, want the sum of chunks", files[0].Size)
	}
}

func TestDepotManifestRejectsBadHash(t *testing.T) {
	c := NewClient(DefaultConfig(), Tokens{AccessToken: "t"})
	if _, err := c.DepotManifest(context.Background(), "../../etc/passwd"); err == nil {
		t.Fatal("want an error for a manifest hash that is not a hash")
	}
}

func TestAcquireNeedsStagingDir(t *testing.T) {
	s := newStubContent(t)
	s.happyProduct()
	var cfgErr *store.ConfigError
	_, err := s.client().acquire(context.Background(), product, "", store.AcquireOptions{})
	if err == nil || !errors.As(err, &cfgErr) {
		t.Fatalf("err = %v, want a config error", err)
	}
}
