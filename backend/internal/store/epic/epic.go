// Package epic is the Epic Games Store adapter: sign-in, entitled-library listing,
// build resolution and binary install-manifest decoding.
//
// Like the GOG adapter this is a Go reimplementation of behaviour this project worked
// out live in its archived predecessor (ADR-0009). The parts worth knowing are recorded
// where they are used: the launcher client pair, the fact that entitlements carry no
// display names, the redirect-page JSON that users paste instead of a code, and Epic's
// error body shape with its approval link.
//
// Acquisition resolves a build through the launcher content service, downloads its
// proprietary binary install manifest, decodes it, and stages the install tree from the
// chunk extents the manifest declares. The binary layout is reverse engineered rather
// than officially specified - Epic publishes no format document - so the decoder fails
// loudly through its sentinels instead of guessing; see ParseEpicBinaryManifest.
package epic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cmspeedau/gamedepot/internal/store"
)

const (
	// The launcher client, live-verified: identical to the pair Legendary's EPCAPI
	// authenticates with. An older public pair now answers "invalid client credentials"
	// to the same probe, so this is not a value to "modernise" without re-checking.
	clientID     = "34a02cf8f4414e29b15921876da36f9a"
	clientSecret = "daafbccc737745039dffe53d94fc76cf"

	defaultUserAgent = "GameDepot/1.0 (+https://github.com/cmspeedau/GameDepot)"
)

// Endpoints. Launcher hosts move; these are the ones live as of Nov 2026, overridable
// in Config for tests.
type Config struct {
	OAuthURL   string // account-public-service: token exchange and refresh
	LibraryURL string // library-service: the product records with titles
	CatalogURL string // catalog-service: names, in bulk per namespace
	AssetsURL  string // launcher-public-service: build manifest location + chunk bases
	HTTPClient *http.Client
	Timeout    time.Duration
}

func DefaultConfig() Config {
	return Config{
		OAuthURL:   "https://account-public-service-prod03.ol.epicgames.com/account/api/oauth/token",
		LibraryURL: "https://library-service.live.use1a.on.epicgames.com/library/api/public/items",
		CatalogURL: "https://catalog-public-service-prod06.ol.epicgames.com/catalog/api/shared/namespace",
		AssetsURL:  "https://launcher-public-service-prod06.ol.epicgames.com/launcher/api/public/assets/v2/platform",
		Timeout:    30 * time.Second,
	}
}

// Tokens is one Epic session.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	AccountID    string `json:"account_id"`
	TokenType    string `json:"token_type"`
}

type Client struct {
	cfg    Config
	tokens Tokens
}

func NewClient(cfg Config, tokens Tokens) *Client {
	if cfg.OAuthURL == "" || cfg.LibraryURL == "" || cfg.CatalogURL == "" || cfg.AssetsURL == "" {
		d := DefaultConfig()
		if cfg.OAuthURL == "" {
			cfg.OAuthURL = d.OAuthURL
		}
		if cfg.LibraryURL == "" {
			cfg.LibraryURL = d.LibraryURL
		}
		if cfg.CatalogURL == "" {
			cfg.CatalogURL = d.CatalogURL
		}
		if cfg.AssetsURL == "" {
			cfg.AssetsURL = d.AssetsURL
		}
	}
	if cfg.HTTPClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		cfg.HTTPClient = &http.Client{Timeout: timeout}
	}
	return &Client{cfg: cfg, tokens: tokens}
}

// Name satisfies store.Provider.
func (c *Client) Name() string { return "epic" }

// AuthorizeURL is the Epic sign-in link. Epic has no registered redirect for a
// self-hosted callback under this client, so the flow ends on Epic's own redirect API
// page, which shows the code (as JSON) for the admin to copy.
func AuthorizeURL() string {
	target := "https://www.epicgames.com/id/api/redirect?clientId=" + clientID + "&responseType=code"
	return "https://www.epicgames.com/id/login?redirectUrl=" + url.QueryEscape(target)
}

// ExtractAuthCode accepts the bare code, the redirect page's URL, or the whole JSON body
// the browser shows at /id/api/redirect, which is what people actually select and copy.
// A JSON body carrying an error rather than a code is reported with its correlation id
// instead of being sent to Epic as a code.
func ExtractAuthCode(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "{") {
		var body struct {
			Code          string `json:"code"`
			Error         string `json:"error"`
			CorrelationID string `json:"correlationId"`
		}
		if err := json.Unmarshal([]byte(trimmed), &body); err == nil {
			if body.Code != "" {
				return body.Code, nil
			}
			if body.Error != "" || body.CorrelationID != "" {
				return "", store.Configf("Epic's sign-in page reported %s instead of a code (correlationId %s); open the sign-in page again and copy the code value",
					firstNonEmpty(body.Error, "an error"), firstNonEmpty(body.CorrelationID, "unknown"))
			}
		}
	}
	if strings.Contains(trimmed, "code=") {
		if u, err := url.Parse(trimmed); err == nil {
			if code := u.Query().Get("code"); code != "" {
				return code, nil
			}
		}
	}
	if trimmed == "" {
		return "", store.Configf(`paste the code value from the Epic sign-in page (the whole page also works)`)
	}
	return trimmed, nil
}

// epicError renders Epic's error body. Epic answers failures with
// {errorCode, errorMessage} and, when a human step is required, a correctiveAction plus
// a continuationUrl; dropping those turns every refusal into a guess.
func epicError(status int, body []byte) error {
	var parsed struct {
		ErrorCode        string `json:"errorCode"`
		ErrorMessage     string `json:"errorMessage"`
		CorrectiveAction string `json:"correctiveAction"`
		ContinuationURL  string `json:"continuationUrl"`
	}
	detail := ""
	if json.Unmarshal(body, &parsed) == nil {
		parts := make([]string, 0, 2)
		for _, s := range []string{parsed.ErrorCode, parsed.ErrorMessage} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		detail = strings.Join(parts, ": ")
		if parsed.CorrectiveAction != "" && parsed.ContinuationURL != "" {
			detail = fmt.Sprintf("%s (%s) - open %s to approve, then retry",
				firstNonEmpty(detail, "corrective action required"), parsed.CorrectiveAction, parsed.ContinuationURL)
		}
	}
	if detail == "" {
		detail = firstLine(body, 140)
	}
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return fmt.Errorf("epic: request failed (%d): %s", status, detail)
}

func firstLine(body []byte, max int) string {
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *Client) postToken(ctx context.Context, form url.Values, label string) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.OAuthURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+basicAuth(clientID, clientSecret))
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("epic: could not reach %s: %w", label, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Tokens{}, fmt.Errorf("epic: %s response unreadable", label)
	}
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, epicError(resp.StatusCode, body)
	}
	var tokens Tokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return Tokens{}, errors.New("epic: token response was not readable")
	}
	if tokens.AccessToken == "" {
		return Tokens{}, errors.New("epic: token response carried no access token")
	}
	return tokens, nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// ExchangeCode trades a pasted sign-in code for a session. Codes are single use.
func ExchangeCode(ctx context.Context, cfg Config, input string) (Tokens, error) {
	code, err := ExtractAuthCode(input)
	if err != nil {
		return Tokens{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("token_type", "eg1")
	return NewClient(cfg, Tokens{}).postToken(ctx, form,
		"Epic code exchange (the code is single-use - sign in again for a fresh one)")
}

// Refresh renews a session. Epic rotates the refresh token, so the whole pair replaces
// the stored one.
func (c *Client) Refresh(ctx context.Context) (Tokens, error) {
	if c.tokens.RefreshToken == "" {
		return Tokens{}, store.Configf("an Epic refresh token is required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", c.tokens.RefreshToken)
	form.Set("token_type", "eg1")
	return c.postToken(ctx, form, "Epic token refresh")
}

// --- library ---

// Entitlement is one row from Epic's entitlement service.
type Entitlement struct {
	ID              string `json:"id"`
	EntitlementName string `json:"entitlementName"`
	Namespace       string `json:"namespace"`
	CatalogItemID   string `json:"catalogItemId"`
	DisplayName     string `json:"displayName"`
	EntitlementType string `json:"entitlementType"`
}

// LibraryRecord is one product record from Epic's library service.
type LibraryRecord struct {
	Namespace     string `json:"namespace"`
	CatalogItemID string `json:"catalogItemId"`
	AppName       string `json:"appName"`
	SandboxName   string `json:"sandboxName"`
	Platform      string `json:"platform"`
	RecordType    string `json:"recordType"`
	SandboxType   string `json:"sandboxType"`
}

// Item is a listable product: named, deduplicated, carrying what acquisition needs.
type Item struct {
	Namespace     string `json:"namespace"`
	CatalogItemID string `json:"catalogItemId"`
	Title         string `json:"title"`
	AppName       string `json:"appName"`
	SandboxName   string `json:"sandboxName,omitempty"`
	Platforms     []string
}

// gameEntitlementTypes are the entitlement types that read as titles.
var gameEntitlementTypes = map[string]bool{"EXECUTABLE": true, "BASE_GAME": true, "ADDON": true}

// isRuntime reports whether a record is Epic's own machinery rather than a game: the
// launcher, the UE_* engine builds, and anything in the `ue` namespace. Live-verified,
// the ue namespace holds engine builds plus dozens of hash-named freebie grants, and
// showing that wall of hex as a library is exactly what the page must not do.
func isRuntime(appName, namespace string) bool {
	return appName == "EpicGamesLauncher" || strings.HasPrefix(appName, "UE_") || namespace == "ue"
}

// fabListing matches Epic's raw Fab marketplace placeholder sandbox names
// ("fab-listing-live"), which are engine assets, not games.
var fabListing = regexp.MustCompile(`(?i)^fab-`)

func listable(items []LibraryRecord) []LibraryRecord {
	seen := map[string]bool{}
	out := make([]LibraryRecord, 0, len(items))
	for _, item := range items {
		if isRuntime(item.AppName, item.Namespace) {
			continue
		}
		if fabListing.MatchString(strings.TrimSpace(item.SandboxName)) {
			continue
		}
		key := item.Namespace + "/" + item.CatalogItemID
		if seen[key] {
			continue // one row per product, however many platform records Epic carries
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

// Library returns the account's games.
//
// The library service is the spine: entitlements answer what is entitled and say nothing
// about names (live-verified: displayName is null for every row, purchased titles
// included), while library records carry the install appName and sandboxName. Titles come
// from the catalog service where it answers, with the record's own name as the free
// second source.
func (c *Client) Library(ctx context.Context) ([]store.Game, error) {
	records, err := c.libraryRecords(ctx)
	if err != nil {
		return nil, err
	}
	keep := listable(records)
	names, nameErr := c.catalogNames(ctx, keep)
	if nameErr != nil {
		// Names are a nicety; the products are the answer. A catalog outage must not
		// empty the library, so fall back to the record's own sandbox name.
		names = map[string]catalogName{}
	}
	games := make([]store.Game, 0, len(keep))
	for _, r := range keep {
		title := firstNonEmpty(names[r.CatalogItemID].Title, strings.TrimSpace(r.SandboxName),
			r.AppName, r.CatalogItemID)
		games = append(games, store.Game{
			ID:    r.Namespace + "/" + r.CatalogItemID,
			Title: title,
		})
	}
	return games, nil
}

func (c *Client) libraryRecords(ctx context.Context) ([]LibraryRecord, error) {
	var out []LibraryRecord
	cursor := ""
	// Generous page cap: Epic's cursors run ~450 records each and even decade-old
	// accounts sit far below 25 pages, but a looping cursor must not hang the request.
	for page := 0; page < 25; page++ {
		endpoint := c.cfg.LibraryURL
		if cursor != "" {
			endpoint += "?cursor=" + url.QueryEscape(cursor)
		}
		body, err := c.authorizedGet(ctx, endpoint, "Epic library request failed")
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Records          []LibraryRecord `json:"records"`
			ResponseMetadata struct {
				NextCursor string `json:"nextCursor"`
			} `json:"responseMetadata"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, errors.New("epic: library returned an unexpected response")
		}
		out = append(out, parsed.Records...)
		cursor = parsed.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
	}
	return out, nil
}

type catalogName struct {
	Title   string
	AppName string
}

// catalogNames resolves titles in one bulk call per namespace. The ids are Epic's
// machine hashes for base games, so this lookup is the difference between a readable
// library and a wall of hex.
func (c *Client) catalogNames(ctx context.Context, items []LibraryRecord) (map[string]catalogName, error) {
	byNamespace := map[string][]string{}
	for _, item := range items {
		byNamespace[item.Namespace] = append(byNamespace[item.Namespace], item.CatalogItemID)
	}
	out := map[string]catalogName{}
	for namespace, ids := range byNamespace {
		endpoint := fmt.Sprintf("%s/%s/bulk/items?count=%d&allow_unpublished=true&allow_free=true",
			c.cfg.CatalogURL, url.PathEscape(namespace), len(ids)+10)
		for _, id := range ids {
			endpoint += "&id=" + url.QueryEscape(id)
		}
		body, err := c.authorizedGet(ctx, endpoint, "Epic catalog request failed")
		if err != nil {
			return out, err
		}
		var parsed struct {
			Elements []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				AppName string `json:"appName"`
			} `json:"elements"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			continue // a namespace we cannot read simply keeps its sandbox names
		}
		for _, e := range parsed.Elements {
			out[e.ID] = catalogName{Title: e.Title, AppName: e.AppName}
		}
	}
	return out, nil
}

func (c *Client) authorizedGet(ctx context.Context, endpoint, failure string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.tokens.AccessToken)
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("epic: %s: %w", failure, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("epic: %w", store.ErrReauth)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, epicError(resp.StatusCode, body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("epic: %s: response unreadable", failure)
	}
	return body, nil
}

// AssetInfo is one build's manifest location and chunk distribution bases.
type AssetInfo struct {
	Label             string   `json:"label"`
	BuildVersion      string   `json:"buildVersion"`
	AppName           string   `json:"appName"`
	CatalogItemID     string   `json:"catalogItemId"`
	Namespace         string   `json:"namespace"`
	ManifestName      string   `json:"manifestName"`
	ManifestLocation  string   `json:"manifestLocation"`
	DistributionPoint string   `json:"distributionPoint"`
	ChunkBaseDURLs    []string `json:"chunkBaseDurls"`
	Elements          []struct {
		ManifestLocation string `json:"manifestLocation"`
		Meta             struct {
			ChunkBaseURLs []string `json:"chunkBaseUrls"`
		} `json:"meta"`
	} `json:"elements"`
}

// Asset resolves the build manifest location and chunk CDN bases for a product.
// A 404 means the app has no build for that platform, and is reported as "not found"
// rather than as an outage.
func (c *Client) Asset(ctx context.Context, platform, namespace, catalogItemID, appName string) (*AssetInfo, error) {
	endpoint := fmt.Sprintf("%s/%s?namespace=%s&catalogItemId=%s&appName=%s",
		c.cfg.AssetsURL, url.PathEscape(firstNonEmpty(platform, "Windows")),
		url.QueryEscape(namespace), url.QueryEscape(catalogItemID), url.QueryEscape(appName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.tokens.AccessToken)
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("epic: launcher assets query failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, errors.New("epic: launcher assets response unreadable")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, epicError(resp.StatusCode, body)
	}
	var parsed struct {
		Elements []AssetInfo `json:"elements"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Elements) == 0 {
		return nil, errors.New("epic: launcher assets carried no build")
	}
	return &parsed.Elements[0], nil
}

// Acquire resolves the build an Epic product would install, downloads and decodes its
// binary install manifest, and stages the install tree under the caller's staging
// directory. Files without chunk extents are skipped, exactly like the GOG adapter skips
// directories and links.
func (c *Client) Acquire(ctx context.Context, productID string, opts store.AcquireOptions) (store.Result, error) {
	namespace, catalogItemID, ok := strings.Cut(productID, "/")
	if !ok {
		return store.Result{}, store.Configf("an Epic product id is <namespace>/<catalogItemId>")
	}
	asset, err := c.Asset(ctx, platformName(opts.Platform), namespace, catalogItemID, catalogItemID)
	if err != nil {
		return store.Result{}, err
	}
	if asset == nil {
		return store.Result{}, fmt.Errorf("epic: no build for %s on %s", productID, opts.Platform)
	}
	if opts.StagingDir == "" {
		return store.Result{}, store.Configf("a staging directory is required")
	}
	manifestBytes, err := c.fetchEpicManifest(ctx, asset)
	if err != nil {
		return store.Result{}, err
	}
	parsed, err := ParseEpicBinaryManifest(manifestBytes)
	if err != nil {
		return store.Result{}, fmt.Errorf("epic: build %s: %w (manifest: %s)", asset.BuildVersion, err, asset.ManifestLocation)
	}
	if len(parsed.Files) == 0 {
		return store.Result{}, fmt.Errorf("epic: build %s lists no files to install", asset.BuildVersion)
	}

	versionName := firstNonEmpty(asset.BuildVersion, asset.Label)
	versionDir := filepath.Join(opts.StagingDir, versionName)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return store.Result{}, fmt.Errorf("epic: staging: %w", err)
	}

	bases := epicChunkBases(asset)
	var (
		total   int64
		written int
	)
	for i := range parsed.Files {
		file := parsed.Files[i]
		if len(file.ChunkParts) == 0 {
			continue // directories, links and empty entries carry no bytes
		}
		n, err := c.writeEpicFile(ctx, versionDir, file, bases)
		if err != nil {
			return store.Result{}, err
		}
		total += n
		written++
	}
	if written == 0 {
		return store.Result{}, fmt.Errorf("epic: build %s staged no files", asset.BuildVersion)
	}

	return store.Result{
		Dir:         versionDir,
		Files:       written,
		TotalSize:   total,
		BuildID:     firstNonEmpty(asset.BuildVersion, asset.Label),
		VersionName: versionName,
		Launches:    epicLaunches(&parsed.Meta),
	}, nil
}

// epicChunkBases collects the CDN mirrors an Epic chunk may be served from, most
// specific first and with the distribution point as the documented fallback.
func epicChunkBases(asset *AssetInfo) []string {
	var bases []string
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" || u == "/" {
			return
		}
		for _, existing := range bases {
			if existing == u {
				return
			}
		}
		bases = append(bases, u)
	}
	for _, u := range asset.ChunkBaseDURLs {
		add(u)
	}
	for i := range asset.Elements {
		for _, u := range asset.Elements[i].Meta.ChunkBaseURLs {
			add(u)
		}
	}
	add(asset.DistributionPoint)
	return bases
}

// fetchEpicManifest downloads one build's binary install manifest and bounds the read
// to the same envelope the decoder accepts, so a hostile or corrupt response cannot make
// the ingest buffer without limit.
func (c *Client) fetchEpicManifest(ctx context.Context, asset *AssetInfo) ([]byte, error) {
	target := asset.ManifestLocation
	if target == "" {
		return nil, errors.New("epic: resolved build carries no manifest location")
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("epic: manifest location %q is not an http(s) URL", target)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("epic: manifest request failed: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+c.tokens.AccessToken)
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("epic: manifest download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("epic: %w", store.ErrReauth)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, epicError(resp.StatusCode, body)
	}
	if declared := resp.Header.Get("Content-Length"); declared != "" {
		if n, err := strconv.ParseInt(declared, 10, 64); err == nil && n > epicManifestMaxBytes {
			return nil, fmt.Errorf("epic: manifest exceeds the accepted size limit (%d bytes)", n)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, epicManifestMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("epic: manifest response unreadable: %w", err)
	}
	if int64(len(body)) > epicManifestMaxBytes {
		return nil, fmt.Errorf("epic: manifest exceeds the accepted size limit (%d bytes)", len(body))
	}
	return body, nil
}

// downloadEpicChunk fetches one content-addressed chunk, trying each CDN mirror and
// both of Epic's chunk path layouts in turn.
func (c *Client) downloadEpicChunk(ctx context.Context, bases []string, guid [4]uint32) ([]byte, error) {
	normalized := strings.ToLower(fmt.Sprintf("%08x%08x%08x%08x", guid[0], guid[1], guid[2], guid[3]))
	variants := []string{
		"ChunksV4/" + normalized[:2] + "/" + normalized + ".chunk",
		normalized + ".chunk",
	}
	for _, base := range bases {
		clean := strings.TrimRight(base, "/")
		for _, variant := range variants {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, clean+"/"+variant, nil)
			if err != nil {
				return nil, fmt.Errorf("epic: chunk %x request failed: %w", guid, err)
			}
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Authorization", "Bearer "+c.tokens.AccessToken)
			req.Header.Set("User-Agent", defaultUserAgent)
			resp, err := c.cfg.HTTPClient.Do(req)
			if err != nil {
				continue // try the next mirror
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				continue
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("epic: chunk %x unreadable: %w", guid, err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("epic: no chunk mirror served chunk %s", normalized)
}

// writeEpicFile assembles one decoded file from its chunk extents and moves it into
// the staging tree. The assembled bytes are verified against the declared file size and
// the unix executable flag from the manifest before the file is put in place.
func (c *Client) writeEpicFile(ctx context.Context, root string, file EpicManifestFile, bases []string) (int64, error) {
	if len(file.ChunkParts) == 0 {
		return 0, nil
	}
	if err := unsafeEpicPath(file.Path); err != nil {
		return 0, err
	}
	data := make([]byte, file.FileSize)
	for _, part := range file.ChunkParts {
		chunk, err := c.downloadEpicChunk(ctx, bases, part.Guid)
		if err != nil {
			return 0, fmt.Errorf("epic: %s: %w", file.Path, err)
		}
		if int64(len(chunk)) > int64(part.Size) {
			return 0, fmt.Errorf("epic: %s: chunk %x is %d bytes, manifest declared %d", file.Path, part.Guid, len(chunk), part.Size)
		}
		if int64(part.FileOffset)+int64(part.Size) > int64(file.FileSize) {
			return 0, fmt.Errorf("epic: %s: chunk at offset %d exceeds file size %d", file.Path, part.FileOffset, file.FileSize)
		}
		copy(data[part.FileOffset:], chunk)
	}

	target := filepath.Join(root, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("epic: staging: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".epic-*")
	if err != nil {
		return 0, fmt.Errorf("epic: staging: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once the rename succeeded
	}()
	if _, err := tmp.Write(data); err != nil {
		return 0, fmt.Errorf("epic: staging: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return 0, fmt.Errorf("epic: staging: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("epic: staging: %w", err)
	}
	mode := os.FileMode(0o644)
	if file.Flags&epicUnixExecutable != 0 {
		mode = os.FileMode(0o755)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return 0, fmt.Errorf("epic: staging: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return 0, fmt.Errorf("epic: staging: %w", err)
	}
	return file.FileSize, nil
}

// epicLaunches turns the launch fields of a decoded manifest into the launch entries
// the release is seeded with. Builds that declare no executable install nothing playable.
func epicLaunches(meta *EpicManifestMeta) []store.Launch {
	if meta == nil || meta.LaunchExe == "" {
		return nil
	}
	args := []string{}
	if meta.LaunchCommand != "" {
		args = strings.Fields(meta.LaunchCommand)
	}
	return []store.Launch{{
		Name: firstNonEmpty(meta.AppName, "Play"),
		Exe:  meta.LaunchExe,
		Args: args,
	}}
}

// unsafeEpicPath validates a manifest-declared file path before it reaches the staging
// tree: relative, forward-slash separated, no traversal, no drive letters.
func unsafeEpicPath(path string) error {
	clean := filepath.ToSlash(path)
	if clean == "" || clean == "." || clean == "/" || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("epic: unsafe file path %q", path)
	}
	if len(clean) >= 2 && clean[1] == ':' {
		return fmt.Errorf("epic: unsafe file path %q", path)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("epic: unsafe file path %q", path)
		}
	}
	return nil
}

// platformName maps this project's platform keys onto Epic's.
func platformName(p string) string {
	switch strings.ToLower(p) {
	case "linux":
		return "Linux"
	case "macos", "mac":
		return "Mac"
	default:
		return "Windows"
	}
}

// LatestBuild resolves the build label Epic would serve, so an update check can answer
// without decoding anything.
func (c *Client) LatestBuild(ctx context.Context, productID, platform string) (store.BuildInfo, error) {
	namespace, catalogItemID, ok := strings.Cut(productID, "/")
	if !ok {
		return store.BuildInfo{}, store.Configf("an Epic product id is <namespace>/<catalogItemId>")
	}
	asset, err := c.Asset(ctx, platformName(platform), namespace, catalogItemID, catalogItemID)
	if err != nil {
		return store.BuildInfo{}, err
	}
	if asset == nil {
		return store.BuildInfo{}, fmt.Errorf("epic: no build for %s", productID)
	}
	return store.BuildInfo{ID: asset.BuildVersion, VersionName: asset.Label}, nil
}
