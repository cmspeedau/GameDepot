package gog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/cmspeedau/gamedepot/internal/store"
)

// fileConcurrency keeps the download on a handful of connections; GOG is politely
// tolerant of four concurrent files from one client.
const fileConcurrency = 4

// Name satisfies store.Provider.
func (c *Client) Name() string { return "gog" }

// infoExecutable is one launch entry in GOG's classic Galaxy info document.
type infoExecutable struct {
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	Type      string          `json:"type"`
	Arguments json.RawMessage `json:"arguments"`
}

// playTask is one entry in the newer content-system info document.
type playTask struct {
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	Category  string          `json:"category"`
	Type      string          `json:"type"`
	Arguments json.RawMessage `json:"arguments"`
}

// ParseGogInfo reads a GOG game info document and returns its install directory and
// launchable executables.
//
// Two live-verified schemas exist: the classic Galaxy `goggame-<id>.info.json` with
// `executables[]` (type "Game", plateau-era documents nest them under `plateau`), and
// the newer content-system `goggame-<id>.info` whose `playTasks[]` mark launchers with
// category "game" and type "FileTask".
func ParseGogInfo(document []byte) (installDir string, launches []store.Launch, err error) {
	var doc struct {
		InstallDirectory string           `json:"installDirectory"`
		Executables      []infoExecutable `json:"executables"`
		Plateau          struct {
			Executables []infoExecutable `json:"executables"`
		} `json:"plateau"`
		PlayTasks []playTask `json:"playTasks"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return "", nil, errors.New("gog: the GOG game info document is not readable JSON")
	}
	declared := append([]infoExecutable{}, doc.Executables...)
	declared = append(declared, doc.Plateau.Executables...)
	for _, task := range doc.PlayTasks {
		if task.Category == "game" && task.Path != "" {
			declared = append(declared, infoExecutable{
				Name: task.Name, Path: task.Path, Type: "Game", Arguments: task.Arguments,
			})
		}
	}
	for _, exe := range declared {
		if exe.Type != "Game" || exe.Path == "" {
			continue
		}
		name := exe.Name
		if name == "" {
			name = "Play"
		}
		launches = append(launches, store.Launch{
			Name: name,
			Exe:  normalizeDepotPath(exe.Path),
			Args: toArgs(exe.Arguments),
		})
	}
	return doc.InstallDirectory, launches, nil
}

// toArgs accepts the two shapes GOG uses for launch arguments: a bare string or an
// array of strings.
func toArgs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		if single == "" {
			return nil
		}
		return []string{single}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}

// unsafePath rejects a depot path that could escape the staging directory.
func unsafePath(path string) error {
	clean := strings.TrimLeft(path, "/")
	if clean == "" {
		return fmt.Errorf("gog: GOG depot contains an empty file path")
	}
	if strings.Contains(clean, ":") || strings.Contains(clean, "\\") {
		return fmt.Errorf("gog: GOG depot contains an unsafe file path: %s", path)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return fmt.Errorf("gog: GOG depot contains an unsafe file path: %s", path)
		}
	}
	return nil
}

var unsafeVersionChars = regexp.MustCompile(`[^A-Za-z0-9._+-]`)

// pickBuild chooses the build to acquire. The list is newest-first across *all*
// branches, so a Master filter is applied first: an Experimental build published later
// must not hijack the acquisition. Falls back to the overall newest when nothing looks
// like the stable branch.
func pickBuilds(builds []Build, wantID string) (Build, error) {
	if wantID != "" {
		for _, b := range builds {
			if b.BuildID == wantID {
				return b, nil
			}
		}
		return Build{}, fmt.Errorf("gog: no GOG build %s in this product", wantID)
	}
	for _, b := range builds {
		if b.Branch == "" || b.Branch == "Master" {
			return b, nil
		}
	}
	if len(builds) > 0 {
		return builds[0], nil
	}
	return Build{}, errors.New("gog: this GOG product has no downloadable build")
}

// selectDepots keeps the base game's 64-bit depots for one language.
//
//   - A depot belonging to another product id is DLC; its entitlement is separate and
//     out of scope for base-game acquisition.
//   - Language: a depot with no declared language, or "*" plus the wanted one, is kept.
//   - Bitness: content that is 32-bit only is skipped.
func selectDepots(meta BuildMeta, baseProductID, language string) []Depot {
	if language == "" {
		language = "en-US"
	}
	var out []Depot
	for _, d := range meta.Depots {
		if d.Manifest == "" {
			continue
		}
		if d.ProductID != "" && d.ProductID != baseProductID {
			continue
		}
		if len(d.Languages) > 0 && !containsAny(d.Languages, "*", language) {
			continue
		}
		if len(d.OSBitness) > 0 && !containsAny(d.OSBitness, "64") {
			continue
		}
		out = append(out, d)
	}
	return out
}

func containsAny(values []string, want ...string) bool {
	for _, v := range values {
		for _, w := range want {
			if v == w {
				return true
			}
		}
	}
	return false
}

// Acquire downloads one product's files into dest/<versionName>/ and reports what it
// wrote. The result is a plain directory tree: the caller hands it to
// internal/depot.BuildRelease like any other library source.
//
// The launch document is read before the bulk download starts, because a game nobody
// can launch is not worth gigabytes.
func (c *Client) Acquire(ctx context.Context, productID string, opts store.AcquireOptions) (store.Result, error) {
	return c.acquire(ctx, productID, "", opts)
}

func (c *Client) acquire(ctx context.Context, productID, buildID string, opts store.AcquireOptions) (store.Result, error) {
	var result store.Result
	if !ValidProductID(productID) {
		return result, store.Configf("a valid GOG game ID is required")
	}
	if opts.Platform == "" {
		opts.Platform = "windows"
	}
	if opts.Language == "" {
		opts.Language = "en-US"
	}

	builds, err := c.Builds(ctx, productID, opts.Platform)
	if err != nil {
		return result, err
	}
	build, err := pickBuilds(builds, buildID)
	if err != nil {
		return result, err
	}
	meta, err := c.BuildMeta(ctx, build.Link)
	if err != nil {
		return result, err
	}
	baseProduct := meta.BaseProductID
	if baseProduct == "" {
		baseProduct = productID
	}
	depots := selectDepots(meta, baseProduct, opts.Language)
	if len(depots) == 0 {
		return result, fmt.Errorf("gog: build %s exposes no depot manifest for %s/%s/x64",
			build.BuildID, opts.Platform, opts.Language)
	}

	// Multiple depots (content plus small config overlays) apply in order: a later
	// depot overwriting an earlier path is how GOG ships updates.
	byPath := map[string]DepotFile{}
	for _, d := range depots {
		files, err := c.DepotManifest(ctx, d.Manifest)
		if err != nil {
			return result, err
		}
		for _, f := range files {
			if err := unsafePath(f.Path); err != nil {
				return result, err
			}
			byPath[f.Path] = f
		}
	}

	var downloadable []DepotFile
	for path, f := range byPath {
		if len(f.Chunks) == 0 {
			continue // directories and links carry no bytes
		}
		f.Path = path
		downloadable = append(downloadable, f)
	}
	if len(downloadable) == 0 {
		return result, errors.New("gog: no files found in the GOG depot manifests for this build")
	}
	sort.Slice(downloadable, func(i, j int) bool { return downloadable[i].Path < downloadable[j].Path })

	endpoints, err := c.SecureLinks(ctx, productID)
	if err != nil {
		return result, err
	}

	// Launch metadata first.
	infoPath := ""
	wanted := []string{fmt.Sprintf("goggame-%s.info.json", productID), fmt.Sprintf("goggame-%s.info", productID)}
	for path := range byPath {
		base := strings.ToLower(filepath.Base(path))
		for _, w := range wanted {
			if base == strings.ToLower(w) {
				infoPath = path
			}
		}
		if infoPath != "" {
			break
		}
	}
	if infoPath == "" {
		return result, fmt.Errorf("gog: build %s ships no goggame-%s.info(.json) launch document", build.BuildID, productID)
	}
	infoBytes, err := c.fetchWhole(ctx, endpoints, byPath[infoPath])
	if err != nil {
		return result, err
	}
	_, launches, err := ParseGogInfo(infoBytes)
	if err != nil {
		return result, err
	}
	if len(launches) == 0 {
		return result, fmt.Errorf("gog: goggame-%s.info declares no launchable game executable", productID)
	}

	versionName := unsafeVersionChars.ReplaceAllString(firstNonEmpty(build.VersionName, build.BuildID), "_")
	dest := opts.StagingDir
	if dest == "" {
		return result, store.Configf("a staging directory is required")
	}
	versionDir := filepath.Join(dest, versionName)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return result, fmt.Errorf("gog: staging: %w", err)
	}

	var (
		mu       sync.Mutex
		total    int64
		written  int
		failures []error
	)
	sem := make(chan struct{}, fileConcurrency)
	var wg sync.WaitGroup
	for _, f := range downloadable {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(f DepotFile) {
			defer wg.Done()
			defer func() { <-sem }()
			n, err := c.writeFile(ctx, endpoints, versionDir, f)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			total += n
			written++
			if opts.OnProgress != nil {
				opts.OnProgress(total, declaredTotal(downloadable))
			}
		}(f)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(failures) > 0 {
		return result, failures[0]
	}

	return store.Result{
		Dir:         versionDir,
		Files:       written,
		TotalSize:   total,
		BuildID:     build.BuildID,
		VersionName: versionName,
		Launches:    launches,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func declaredTotal(files []DepotFile) int64 {
	var t int64
	for _, f := range files {
		t += f.Size
	}
	return t
}

// fetchWhole concatenates every chunk of a file in memory. Only used for the small
// launch document.
func (c *Client) fetchWhole(ctx context.Context, endpoints []SecureEndpoint, f DepotFile) ([]byte, error) {
	var out []byte
	for _, chunk := range f.Chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bytes, err := c.acquireChunk(ctx, endpoints, f.Path, chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, bytes...)
	}
	return out, nil
}

// writeFile streams one depot file: chunk by chunk into a temp file, verified against
// the manifest size, then renamed into place so a failed acquire leaves no half file.
func (c *Client) writeFile(ctx context.Context, endpoints []SecureEndpoint, root string, f DepotFile) (int64, error) {
	if err := unsafePath(f.Path); err != nil {
		return 0, err
	}
	target := filepath.Join(root, filepath.FromSlash(f.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("gog: %s: %w", f.Path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".gog-*")
	if err != nil {
		return 0, fmt.Errorf("gog: %s: %w", f.Path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once the rename succeeded
	}()

	var written int64
	for _, chunk := range f.Chunks {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		plain, err := c.acquireChunk(ctx, endpoints, f.Path, chunk)
		if err != nil {
			return 0, err
		}
		if _, err := tmp.Write(plain); err != nil {
			return 0, fmt.Errorf("gog: %s: %w", f.Path, err)
		}
		written += int64(len(plain))
	}
	if err := tmp.Sync(); err != nil {
		return 0, fmt.Errorf("gog: %s: %w", f.Path, err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("gog: %s: %w", f.Path, err)
	}
	if f.Size > 0 && written != f.Size {
		return 0, fmt.Errorf("gog: %s size mismatch: wrote %d, manifest declared %d", f.Path, written, f.Size)
	}
	mode := os.FileMode(0o644)
	if f.Executa {
		mode = 0o755
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return 0, fmt.Errorf("gog: %s: %w", f.Path, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return 0, fmt.Errorf("gog: %s: %w", f.Path, err)
	}
	return written, nil
}
