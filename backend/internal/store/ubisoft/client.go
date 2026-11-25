// Package ubisoft is the Ubisoft Connect storefront adapter.
//
// client.go holds the HTTP side of the provider: session creation (with the
// device-check challenge and two-factor exchange), remember-me renewal, and the
// GraphQL ownership query that discovers entitled titles. Install acquisition
// lives on the demux socket (socket.go) and is wired up separately.
package ubisoft

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/google/uuid"

	"github.com/cmspeedau/gamedepot/internal/store"
)

const (
	ubiAppID        = "3ad0af50-b1e5-4e5f-8c69-b1b17d3a4e9a"
	ubiSessionAppID = "f68a4bb5-608a-4ff2-8123-be8ef797e0a6"
	ubiLoginUA      = "GameDepot/1.0 (Ubisoft Connect sessions API)"
	ubiSessionsURL  = "https://public-ubiservices.ubi.com/v3/profiles/sessions"
	ubiGraphQLURL   = "https://public-ubiservices.ubi.com/v1/profiles/me/uplay/graphql"
	ubiOwnedQuery   = "{ viewer { games { nodes { id spaceId name releaseDate } } } }"
)

// Config is the HTTP endpoint set for one Ubisoft client. Empty fields take the
// defaults.
type Config struct {
	SessionsURL string
	GraphQLURL  string
	HTTPClient  *http.Client
	Timeout     time.Duration
}

func DefaultConfig() Config {
	return Config{
		SessionsURL: ubiSessionsURL,
		GraphQLURL:  ubiGraphQLURL,
		Timeout:     20 * time.Second,
	}
}

// Tokens is one Ubisoft session's sealed credentials.
type Tokens struct {
	Ticket           string
	SessionID        string
	UserID           string
	Username         string
	NameOnPlatform   string
	RememberMeTicket string
}

// UbisoftSession is the raw shape Ubisoft returns from the sessions endpoint.
type UbisoftSession struct {
	Ticket           string `json:"ticket"`
	SessionID        string `json:"sessionId"`
	UserID           string `json:"userId"`
	Username         string `json:"username"`
	NameOnPlatform   string `json:"nameOnPlatform"`
	Expiration       string `json:"expiration"`
	ServerTime       string `json:"serverTime"`
	RememberMeTicket string `json:"rememberMeTicket"`
	Profiles         []struct {
		NameOnPlatform string `json:"nameOnPlatform"`
	} `json:"profiles"`
	PlatformType string `json:"platformType"`
}

// TwoFactorRequired is returned instead of a session when Ubisoft demands a code.
type TwoFactorRequired struct {
	TwoFactorTicket string
	Method          string
}

// LoginResult is either a session or a 2FA challenge.
type LoginResult struct {
	Session   *UbisoftSession
	TwoFactor *TwoFactorRequired
}

// UbisoftOwnedTitle is one entitled product.
type UbisoftOwnedTitle struct {
	ID          string `json:"id"`
	SpaceID     string `json:"spaceId"`
	Name        string `json:"name"`
	ReleaseDate string `json:"releaseDate"`
}

// Client is the store.Provider for Ubisoft. Acquisition is not wired yet; Library
// works against the GraphQL ownership endpoint.
type Client struct {
	cfg    Config
	tokens Tokens
	http   *http.Client
}

func NewClient(cfg Config, tokens Tokens) *Client {
	if cfg.SessionsURL == "" {
		cfg.SessionsURL = ubiSessionsURL
	}
	if cfg.GraphQLURL == "" {
		cfg.GraphQLURL = ubiGraphQLURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, tokens: tokens, http: hc}
}

// Name satisfies store.Provider.
func (c *Client) Name() string { return "ubisoft" }

// AuthorizeURL is not used; Ubisoft auth is credential-based.
func AuthorizeURL() string { return "" }

// Tokens returns the client's stored session tokens.
func (c *Client) Tokens() Tokens { return c.tokens }

// CreateUbisoftSession authenticates with email+password, handling the device-check
// challenge and the 2FA exchange transparently where possible.
func CreateUbisoftSession(ctx context.Context, cfg Config, email, password string) (LoginResult, error) {
	if cfg.SessionsURL == "" {
		cfg.SessionsURL = ubiSessionsURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	basic := base64.StdEncoding.EncodeToString([]byte(email + ":" + password))
	headers := map[string]string{
		"Ubi-AppId":     ubiSessionAppID,
		"Content-Type":  "application/json",
		"User-Agent":    ubiLoginUA,
		"Authorization": "Basic " + basic,
	}
	attempt, err := ubiSessionPost(ctx, hc, cfg.SessionsURL, headers)
	if err != nil {
		return LoginResult{}, err
	}
	if attempt.challenge != "" && !isTwoFactorBody(attempt.body) {
		headers["Ubi-Challenge"] = attempt.challenge
		attempt, err = ubiSessionPost(ctx, hc, cfg.SessionsURL, headers)
		if err != nil {
			return LoginResult{}, err
		}
	}
	if isTwoFactorBody(attempt.body) {
		return LoginResult{TwoFactor: &TwoFactorRequired{
			TwoFactorTicket: attempt.body["twoFactorAuthenticationTicket"].(string),
			Method:          twoFactorMethod(attempt.body),
		}}, nil
	}
	sess, err := readUbisoftSession(attempt)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Session: &sess}, nil
}

func twoFactorMethod(body map[string]any) string {
	if m, ok := body["twoFactorAuthenticationMethod"].(string); ok && m != "" {
		return m
	}
	return "unknown"
}

func isTwoFactorBody(body map[string]any) bool {
	_, ok := body["twoFactorAuthenticationTicket"].(string)
	return ok
}

type ubiAttempt struct {
	status    int
	body      map[string]any
	challenge string
}

func ubiSessionPost(ctx context.Context, hc *http.Client, url string, headers map[string]string) (ubiAttempt, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{"rememberMe":true}`)))
	if err != nil {
		return ubiAttempt{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ubiAttempt{}, fmt.Errorf("ubisoft: authentication service unreachable: %w", err)
	}
	defer resp.Body.Close()
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return ubiAttempt{status: resp.StatusCode, body: body, challenge: resp.Header.Get("Ubi-Challenge")}, nil
}

func readUbisoftSession(attempt ubiAttempt) (UbisoftSession, error) {
	raw, _ := json.Marshal(attempt.body)
	var sess UbisoftSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return UbisoftSession{}, fmt.Errorf("ubisoft: session decode: %w", err)
	}
	if attempt.status != http.StatusOK || sess.Ticket == "" {
		msg, _ := attempt.body["message"].(string)
		if attempt.status == http.StatusUnauthorized {
			if msg == "" {
				msg = "Ubisoft rejected the email or password"
			}
			return UbisoftSession{}, fmt.Errorf("ubisoft: %s", msg)
		}
		botWall := attempt.status == http.StatusForbidden && attempt.body["message"] == nil && attempt.body["environment"] == nil
		suffix := ""
		if botWall {
			suffix = ": blocked by Ubisoft's bot protection"
		} else if msg != "" {
			suffix = ": " + msg
		}
		return UbisoftSession{}, fmt.Errorf("ubisoft: authentication failed (%d)%s", attempt.status, suffix)
	}
	if sess.UserID == "" {
		return UbisoftSession{}, errors.New("ubisoft: session returned without a user id")
	}
	return sess, nil
}

// CompleteUbisoftTwoFactor exchanges a 2FA ticket + code for a real session.
func CompleteUbisoftTwoFactor(ctx context.Context, cfg Config, code, ticket string) (UbisoftSession, error) {
	if code == "" || ticket == "" {
		return UbisoftSession{}, errors.New("ubisoft: a 2FA code and challenge ticket are required")
	}
	if cfg.SessionsURL == "" {
		cfg.SessionsURL = ubiSessionsURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	headers := map[string]string{
		"Ubi-AppId":     ubiSessionAppID,
		"Content-Type":  "application/json",
		"User-Agent":    ubiLoginUA,
		"Authorization": "ubi_2fa_v1 t=" + ticket,
		"Ubi-2FACode":   code,
	}
	attempt, err := ubiSessionPost(ctx, hc, cfg.SessionsURL, headers)
	if err != nil {
		return UbisoftSession{}, err
	}
	return readUbisoftSession(attempt)
}

// LoginUbisoftWithRememberTicket re-opens a session from a long-lived ticket.
func LoginUbisoftWithRememberTicket(ctx context.Context, cfg Config, rememberTicket string) (UbisoftSession, error) {
	if rememberTicket == "" {
		return UbisoftSession{}, errors.New("ubisoft: a remember-me ticket is required")
	}
	if cfg.SessionsURL == "" {
		cfg.SessionsURL = ubiSessionsURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	headers := map[string]string{
		"Ubi-AppId":     ubiSessionAppID,
		"Content-Type":  "application/json",
		"User-Agent":    ubiLoginUA,
		"Authorization": "rm_v1 t=" + rememberTicket,
	}
	attempt, err := ubiSessionPost(ctx, hc, cfg.SessionsURL, headers)
	if err != nil {
		return UbisoftSession{}, err
	}
	return readUbisoftSession(attempt)
}

// RefreshUbisoftSession renews a session by replaying its ticket.
func RefreshUbisoftSession(ctx context.Context, cfg Config, ticket string) (UbisoftSession, error) {
	if ticket == "" {
		return UbisoftSession{}, errors.New("ubisoft: a session ticket is required")
	}
	if cfg.SessionsURL == "" {
		cfg.SessionsURL = ubiSessionsURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.SessionsURL, bytes.NewReader([]byte(`{"rememberMe":true}`)))
	if err != nil {
		return UbisoftSession{}, err
	}
	req.Header.Set("Ubi-AppId", ubiSessionAppID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ubiLoginUA)
	req.Header.Set("Authorization", "Ubi_v1 t="+ticket)
	resp, err := hc.Do(req)
	if err != nil {
		return UbisoftSession{}, fmt.Errorf("ubisoft: refresh unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UbisoftSession{}, fmt.Errorf("ubisoft: refresh failed (%d)", resp.StatusCode)
	}
	var sess UbisoftSession
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return UbisoftSession{}, fmt.Errorf("ubisoft: refresh decode: %w", err)
	}
	return sess, nil
}

// GetUbisoftOwnedTitles lists titles owned by the account via the GraphQL gateway.
func GetUbisoftOwnedTitles(ctx context.Context, cfg Config, ticket string) ([]UbisoftOwnedTitle, error) {
	if ticket == "" {
		return nil, errors.New("ubisoft: a session ticket is required")
	}
	if cfg.GraphQLURL == "" {
		cfg.GraphQLURL = ubiGraphQLURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	body, _ := json.Marshal(map[string]string{"query": ubiOwnedQuery})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.GraphQLURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ubi-AppId", ubiAppID)
	req.Header.Set("Ubi-RequestedPlatformType", "uplaypc")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Ubi_v1 t="+ticket)
	req.Header.Set("Ubi-SessionId", uuid.NewString())
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ubisoft: owned titles unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("ubisoft: owned titles failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var payload struct {
		Data struct {
			Viewer struct {
				Games struct {
					Nodes []UbisoftOwnedTitle `json:"nodes"`
				} `json:"games"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("ubisoft: owned titles decode: %w", err)
	}
	if len(payload.Errors) > 0 {
		e := payload.Errors[0]
		msg := e.Message
		if msg == "" {
			msg = e.Extensions.Code
		}
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("ubisoft: library query failed: %s", msg)
	}
	return payload.Data.Viewer.Games.Nodes, nil
}

// Library implements store.Provider against the GraphQL ownership endpoint.
func (c *Client) Library(ctx context.Context) ([]store.Game, error) {
	titles, err := GetUbisoftOwnedTitles(ctx, c.cfg, c.tokens.Ticket)
	if err != nil {
		return nil, err
	}
	out := make([]store.Game, 0, len(titles))
	for _, t := range titles {
		id := t.SpaceID
		if id == "" {
			id = t.ID
		}
		out = append(out, store.Game{ID: id, Title: t.Name})
	}
	return out, nil
}

// Acquire resolves one Ubisoft product, downloads and decodes its install manifest,
// and stages the install tree under the caller's staging directory.
func (c *Client) Acquire(ctx context.Context, productID string, opts store.AcquireOptions) (store.Result, error) {
	if opts.StagingDir == "" {
		return store.Result{}, store.Configf("a staging directory is required")
	}
	if opts.Language == "" {
		opts.Language = "en-US"
	}
	if opts.Platform == "" {
		opts.Platform = "windows"
	}

	acq, err := ConnectUbisoft(ctx, c.tokens.Ticket, false)
	if err != nil {
		return store.Result{}, err
	}
	defer acq.Destroy()

	games, err := acq.ListOwnedGames(ctx)
	if err != nil {
		return store.Result{}, err
	}
	var game *UbisoftOwnedGame
	for i := range games {
		if games[i].SpaceID == productID || fmt.Sprintf("%d", games[i].ProductID) == productID {
			game = &games[i]
			break
		}
	}
	if game == nil {
		return store.Result{}, fmt.Errorf("ubisoft: product %s is not owned by this account", productID)
	}
	if game.LatestManifest == "" {
		return store.Result{}, fmt.Errorf("ubisoft: product %s publishes no install manifest", productID)
	}

	creds, err := acq.GetDownloadCredentials(ctx, game.ProductID)
	if err != nil {
		return store.Result{}, err
	}
	session, err := acq.OpenDownloads(ctx, game.ProductID, creds)
	if err != nil {
		return store.Result{}, err
	}

	// fetch manifest
	manifestURLs, err := session.MintUrls(ctx, []string{game.LatestManifest})
	if err != nil {
		return store.Result{}, err
	}
	manifestBytes, err := fetchUbisoftBytes(ctx, c.http, manifestURLs[0])
	if err != nil {
		return store.Result{}, err
	}
	m, err := ParseManifest(manifestBytes)
	if err != nil {
		return store.Result{}, err
	}
	plan, err := BuildPlan(m, opts.Language)
	if err != nil {
		return store.Result{}, err
	}
	if len(plan.Files) == 0 {
		return store.Result{}, fmt.Errorf("ubisoft: build %s lists no files to install", game.LatestManifest)
	}

	versionName := strings.ToLower(game.LatestManifest)
	versionDir := filepath.Join(opts.StagingDir, versionName)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return store.Result{}, fmt.Errorf("ubisoft: staging: %w", err)
	}

	// download unique slices, decompress, verify, cache by content hash
	cache := map[string][]byte{}
	downloadSlice := func(s PlanSlice) ([]byte, error) {
		if b, ok := cache[s.VerifySha1]; ok {
			return b, nil
		}
		paths, err := SliceCdnPathCandidates(s.CDNPath)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, p := range paths {
			urls, err := session.MintUrls(ctx, []string{p})
			if err != nil {
				lastErr = err
				continue
			}
			raw, err := fetchUbisoftBytes(ctx, c.http, urls[0])
			if err != nil {
				lastErr = err
				continue
			}
			body, err := decompressUbisoftSlice(raw)
			if err != nil {
				lastErr = err
				continue
			}
			sum := sha1.Sum(body)
			if strings.ToUpper(hex.EncodeToString(sum[:])) != strings.ToUpper(s.VerifySha1) {
				lastErr = fmt.Errorf("ubisoft: slice %s content hash mismatch", s.CDNPath)
				continue
			}
			cache[s.VerifySha1] = body
			return body, nil
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("ubisoft: slice %s: no candidate succeeded", s.CDNPath)
		}
		return nil, lastErr
	}

	// assemble files
	var total int64
	written := 0
	for _, pf := range plan.Files {
		data := make([]byte, pf.Size)
		for _, sl := range pf.Slices {
			body, err := downloadSlice(sl)
			if err != nil {
				return store.Result{}, fmt.Errorf("ubisoft: %s: %w", pf.Path, err)
			}
			if uint64(len(body)) != sl.Size {
				// allow; copy available
			}
			if sl.Offset+uint64(len(body)) > uint64(len(data)) {
				return store.Result{}, fmt.Errorf("ubisoft: %s: slice exceeds file size", pf.Path)
			}
			copy(data[sl.Offset:], body)
		}
		target := filepath.Join(versionDir, filepath.FromSlash(pf.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return store.Result{}, err
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".ubi-*")
		if err != nil {
			return store.Result{}, err
		}
		tmpName := tmp.Name()
		defer func() { tmp.Close(); os.Remove(tmpName) }()
		if _, err := tmp.Write(data); err != nil {
			return store.Result{}, err
		}
		if err := tmp.Sync(); err != nil {
			return store.Result{}, err
		}
		if err := tmp.Close(); err != nil {
			return store.Result{}, err
		}
		if err := os.Rename(tmpName, target); err != nil {
			return store.Result{}, err
		}
		total += int64(pf.Size)
		written++
	}

	launches := make([]store.Launch, 0, len(game.Launches))
	for _, l := range game.Launches {
		launches = append(launches, store.Launch{Name: l.Name, Exe: l.Executable, Args: l.Arguments})
	}

	return store.Result{
		Dir:         versionDir,
		Files:       written,
		TotalSize:   total,
		BuildID:     game.LatestManifest,
		VersionName: versionName,
		Launches:    launches,
	}, nil
}

func fetchUbisoftBytes(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("ubisoft: fetch %s: %d %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}

func decompressUbisoftSlice(body []byte) ([]byte, error) {
	if len(body) >= 4 {
		magic := uint32(body[0]) | (uint32(body[1]) << 8) | (uint32(body[2]) << 16) | (uint32(body[3]) << 24)
		if magic == 0xfd2fb528 {
			dec, err := zstd.NewReader(bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			defer dec.Close()
			return io.ReadAll(dec)
		}
	}
	if len(body) >= 2 && body[0] == 0x28 && body[1] == 0xb5 {
		dec, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		return io.ReadAll(dec)
	}
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		return io.ReadAll(gr)
	}
	if len(body) >= 2 && body[0] == 0x78 {
		zr, err := zlib.NewReader(bytes.NewReader(body))
		if err == nil {
			defer zr.Close()
			if b, err := io.ReadAll(zr); err == nil {
				return b, nil
			}
		}
		// try raw deflate
		fr := flate.NewReader(bytes.NewReader(body))
		defer fr.Close()
		if b, err := io.ReadAll(fr); err == nil {
			return b, nil
		}
	}
	return body, nil
}

// LatestBuild reports the newest install manifest SHA for a product, by running the
// ownership half of the acquisition flow.
func (c *Client) LatestBuild(ctx context.Context, productID, platform string) (store.BuildInfo, error) {
	acq, err := ConnectUbisoft(ctx, c.tokens.Ticket, false)
	if err != nil {
		return store.BuildInfo{}, err
	}
	defer acq.Destroy()
	games, err := acq.ListOwnedGames(ctx)
	if err != nil {
		return store.BuildInfo{}, err
	}
	for _, g := range games {
		if g.SpaceID == productID || fmt.Sprintf("%d", g.ProductID) == productID {
			return store.BuildInfo{ID: g.LatestManifest, VersionName: g.LatestManifest}, nil
		}
	}
	return store.BuildInfo{}, fmt.Errorf("ubisoft: product %s not owned", productID)
}
