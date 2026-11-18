// Package gog is the GOG storefront adapter: sign-in, entitled-library listing, and
// acquisition of a product's files into a staging directory.
//
// The endpoint behaviour encoded here was worked out live against real accounts and is
// reimplemented in Go from this project's own archived predecessor (ADR-0009). The
// non-obvious parts carry the reason, because each of them looks like a mistake to a
// reader who has not seen the failure:
//
//   - GOG accepts exactly one redirect URI for the public client below, and it is not a
//     callback we host. Sign-in therefore ends on a page the admin copies a code from.
//   - Several content endpoints are zlib-compressed with no Content-Encoding header.
//   - Chunk URLs must be signed per product by the secure_link endpoint; the older
//     direct /manifests and /chunks paths 404.
package gog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cmspeedau/gamedepot/internal/store"
)

const (
	// The public client the GOG launcher ecosystem authenticates under. Live-verified:
	// dummy codes reach code validation ("invalid_grant") with this pair, while other
	// public pairs GOG once tolerated fail client authentication outright.
	publicClientID     = "46899977096215655"
	publicClientSecret = "9d85c43b1482497dbbce61f6e4aa173a433796eeae2ca8c5f6129f2dc4de46d9"

	// The only redirect this client is registered for. Every other redirect, localhost
	// callbacks included, gets GOG's "invalid client" page - which is why the code comes
	// back by paste rather than by callback.
	embedRedirectURI = "https://embed.gog.com/on_login_success?origin=client"

	defaultUserAgent = "GameDepot/1.0 (+https://github.com/cmspeedau/GameDepot)"
)

// Endpoints. Fields are overridable so tests can point a Client at an httptest server.
type Config struct {
	AuthURL          string // auth.gog.com
	EmbedURL         string // embed.gog.com (account endpoints)
	ContentSystemURL string // content-system.gog.com (builds, secure links)
	CDNURL           string // gog-cdn-fastly.gog.com (depot metadata, chunk bytes)
	HTTPClient       *http.Client
	Timeout          time.Duration // per request, when HTTPClient is nil
}

func DefaultConfig() Config {
	return Config{
		AuthURL:          "https://auth.gog.com",
		EmbedURL:         "https://embed.gog.com",
		ContentSystemURL: "https://content-system.gog.com",
		CDNURL:           "https://gog-cdn-fastly.gog.com",
		Timeout:          30 * time.Second,
	}
}

// Tokens is one GOG session. RefreshToken rotates on every refresh, so callers must
// write both values back or the next refresh fails.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	AccountID    string
}

// Client performs GOG calls for one authenticated account. accessToken and
// refreshToken are the session the client was built with.
type Client struct {
	cfg          Config
	accessToken  string
	refreshToken string
}

func NewClient(cfg Config, tokens Tokens) *Client {
	if cfg.AuthURL == "" || cfg.EmbedURL == "" || cfg.ContentSystemURL == "" || cfg.CDNURL == "" {
		defaults := DefaultConfig()
		if cfg.AuthURL == "" {
			cfg.AuthURL = defaults.AuthURL
		}
		if cfg.EmbedURL == "" {
			cfg.EmbedURL = defaults.EmbedURL
		}
		if cfg.ContentSystemURL == "" {
			cfg.ContentSystemURL = defaults.ContentSystemURL
		}
		if cfg.CDNURL == "" {
			cfg.CDNURL = defaults.CDNURL
		}
	}
	if cfg.HTTPClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		cfg.HTTPClient = &http.Client{Timeout: timeout, Transport: http.DefaultTransport}
	}
	// Redirects are never followed, even when the caller supplied the client: a 302 from
	// an account endpoint is GOG's way of saying the token is dead, and following it
	// lands on a login page that would parse as a successful 200 of HTML. Copying the
	// client keeps its transport (proxies, TLS config, test servers) and replaces only
	// the redirect policy.
	noRedirect := *cfg.HTTPClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	cfg.HTTPClient = &noRedirect
	return &Client{cfg: cfg, accessToken: tokens.AccessToken, refreshToken: tokens.RefreshToken}
}

// AuthorizeURL is the sign-in link an admin opens. The code arrives on the embed
// redirect page and is pasted back into the dashboard.
func AuthorizeURL() string {
	u := "https://auth.gog.com/auth"
	q := url.Values{}
	q.Set("client_id", publicClientID)
	q.Set("redirect_uri", embedRedirectURI)
	q.Set("response_type", "code")
	q.Set("layout", "galaxy")
	return u + "?" + q.Encode()
}

var gogIDPattern = regexp.MustCompile(`^\d{1,20}$`)

// ValidProductID guards the id before it reaches a URL path or a shell-free download
// list; GOG product ids are decimal.
func ValidProductID(value string) bool { return gogIDPattern.MatchString(strings.TrimSpace(value)) }

// ExtractAuthCode accepts a bare code or the full address-bar URL of the embed
// redirect page, which is what a person copying from a browser actually has.
func ExtractAuthCode(input string) string {
	trimmed := strings.TrimSpace(input)
	if strings.Contains(trimmed, "code=") {
		if u, err := url.Parse(trimmed); err == nil {
			if code := u.Query().Get("code"); code != "" {
				return code
			}
		}
	}
	return trimmed
}

// tokenResponse is GOG's OAuth body. error_description is preserved because GOG
// explains rejections there and swallowing it turns every failure into a guess.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	TokenType        string `json:"token_type"`
	UserID           string `json:"user_id"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (c *Client) token(ctx context.Context, params url.Values) (Tokens, error) {
	endpoint := c.cfg.AuthURL + "/token?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("gog: could not reach the token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Tokens{}, fmt.Errorf("gog: token response unreadable: %w", err)
	}
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr) // a non-JSON body is reported by status below
	if resp.StatusCode != http.StatusOK {
		detail := tr.ErrorDescription
		if detail == "" {
			detail = tr.Error
		}
		if detail == "" {
			detail = firstLine(body, 140)
		}
		return Tokens{}, fmt.Errorf("gog: token endpoint failed (%d): %s", resp.StatusCode, detail)
	}
	if tr.AccessToken == "" || tr.ExpiresIn == 0 {
		return Tokens{}, errors.New("gog: token endpoint returned an invalid token response")
	}
	return Tokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresIn:    tr.ExpiresIn,
		AccountID:    tr.UserID,
	}, nil
}

// ExchangeCode turns a pasted sign-in code into a session. Codes are single use: a
// second attempt on the same code returns invalid_grant, and the message says so.
func (c *Client) ExchangeCode(ctx context.Context, input string) (Tokens, error) {
	code := ExtractAuthCode(input)
	if code == "" {
		return Tokens{}, store.Configf("paste the code from the embed.gog.com/on_login_success page, or its full URL")
	}
	params := url.Values{}
	params.Set("client_id", publicClientID)
	params.Set("client_secret", publicClientSecret)
	params.Set("grant_type", "authorization_code")
	params.Set("code", code)
	params.Set("redirect_uri", embedRedirectURI)
	return c.token(ctx, params)
}

// ExchangeCodeWithConfig performs the code-to-token exchange without an existing
// session; it is how the dashboard completes a sign-in.
func ExchangeCodeWithConfig(ctx context.Context, cfg Config, input string) (Tokens, error) {
	return NewClient(cfg, Tokens{}).ExchangeCode(ctx, input)
}

// Refresh renews the session. GOG rotates the refresh token, so the returned Tokens
// replace the stored pair in full.
func (c *Client) Refresh(ctx context.Context) (Tokens, error) {
	if c.refreshToken == "" {
		return Tokens{}, store.Configf("a GOG refresh token is required to renew the session")
	}
	params := url.Values{}
	params.Set("client_id", publicClientID)
	params.Set("client_secret", publicClientSecret)
	params.Set("grant_type", "refresh_token")
	params.Set("refresh_token", c.refreshToken)
	return c.token(ctx, params)
}

// RefreshWithToken renews a session from a stored refresh token.
func RefreshWithToken(ctx context.Context, cfg Config, refreshToken string) (Tokens, error) {
	return NewClient(cfg, Tokens{RefreshToken: refreshToken}).Refresh(ctx)
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

// --- library ---

type productsPage struct {
	Products   []json.RawMessage `json:"products"`
	TotalPages int               `json:"totalPages"`
	Page       int               `json:"page"`
}

type ownedResponse struct {
	Owned []json.Number `json:"owned"`
}

// accountGet reads one embed.gog.com account endpoint. These take the token in the
// header only; the query-parameter forms redirect to the login page. A redirect or a
// 401/403 means the connection needs signing in again, not a retry.
func (c *Client) accountGet(ctx context.Context, endpoint, failure string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gog: %s: %w", failure, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently ||
		resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("gog: %w", store.ErrReauth)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("gog: %s (%d)", failure, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("gog: %s: response unreadable", failure)
	}
	return body, nil
}

// Library lists the account's owned games.
//
// Three documented endpoints: /user/data/games (ids only),
// /account/getFilteredProducts (the titled, paged spine), and
// /account/gameDetails/<id>.json (the arbiter). The id list is the completeness anchor
// because it also carries products the filtered view deliberately omits; gameDetails
// answers [] for every one of those (DLC, bundle SKUs, soundtracks, delisted
// leftovers), so a top-up only adds rows for products that really are games.
func (c *Client) Library(ctx context.Context) ([]store.Game, error) {
	games, err := c.entitledProducts(ctx)
	if err != nil {
		return nil, err
	}
	// The anchor is worth a top-up, never worth failing the page for.
	owned, ownedErr := c.ownedIDs(ctx)
	if ownedErr != nil || len(owned) == 0 {
		return games, nil
	}
	listed := make(map[string]bool, len(games))
	for _, g := range games {
		listed[g.ID] = true
	}
	const maxTopUp = 120 // one details call each; keeps an admin page short of GOG's patience
	missing := 0
	for _, id := range owned {
		if listed[id] || missing >= maxTopUp {
			continue
		}
		missing++
		g, err := c.productDetails(ctx, id)
		if err != nil {
			continue // a failure costs one row, never the library
		}
		if g != nil {
			games = append(games, *g)
		}
	}
	return games, nil
}

func (c *Client) ownedIDs(ctx context.Context) ([]string, error) {
	body, err := c.accountGet(ctx, c.cfg.EmbedURL+"/user/data/games", "owned-ids request failed")
	if err != nil {
		return nil, err
	}
	var parsed ownedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, errors.New("gog: owned-ids response was not readable")
	}
	out := make([]string, 0, len(parsed.Owned))
	for _, n := range parsed.Owned {
		if s := n.String(); ValidProductID(s) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *Client) entitledProducts(ctx context.Context) ([]store.Game, error) {
	const maxPages = 20
	var games []store.Game
	for page := 1; page <= maxPages; page++ {
		endpoint := fmt.Sprintf("%s/account/getFilteredProducts?mediaType=1&page=%d", c.cfg.EmbedURL, page)
		body, err := c.accountGet(ctx, endpoint, "library request failed")
		if err != nil {
			return nil, err
		}
		var parsed productsPage
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, errors.New("gog: library page was not readable")
		}
		for _, raw := range parsed.Products {
			if g := shapeProduct(raw); g != nil {
				games = append(games, *g)
			}
		}
		total := parsed.TotalPages
		if total == 0 {
			total = page
		}
		if len(parsed.Products) == 0 || page >= total {
			break
		}
	}
	return games, nil
}

// shapeProduct keeps an entry only when it has a usable id and a title; anything else
// is provider noise and is dropped rather than shown as an empty row.
func shapeProduct(raw json.RawMessage) *store.Game {
	var item struct {
		ID      json.Number    `json:"id"`
		Title   string         `json:"title"`
		WorksOn map[string]any `json:"worksOn"`
		Raw     json.RawMessage
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil
	}
	id := item.ID.String()
	if !ValidProductID(id) || item.Title == "" {
		return nil
	}
	g := &store.Game{ID: id, Title: item.Title}
	// getFilteredProducts reports platforms as { "Windows": true, "Mac": false, ... }.
	for name, supported := range item.WorksOn {
		if ok, _ := supported.(bool); ok {
			g.Platforms = append(g.Platforms, strings.ToLower(name))
		}
	}
	return g
}

// productDetails asks the per-product endpoint whether an id is a game at all. GOG
// answers [] for anything that is not (DLC, bundles, extras), so only a titled object
// with downloads earns a row.
func (c *Client) productDetails(ctx context.Context, productID string) (*store.Game, error) {
	if !ValidProductID(productID) {
		return nil, store.Configf("invalid GOG product id")
	}
	body, err := c.accountGet(ctx, fmt.Sprintf("%s/account/gameDetails/%s.json", c.cfg.EmbedURL, productID), "product details request failed")
	if err != nil {
		return nil, err
	}
	var probe struct {
		Title     string            `json:"title"`
		Downloads []json.RawMessage `json:"downloads"`
		WorksOn   map[string]any    `json:"worksOn"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, nil // `[]` decodes as an array, not this object: not a game
	}
	if probe.Title == "" || len(probe.Downloads) == 0 {
		return nil, nil
	}
	g := &store.Game{ID: productID, Title: probe.Title}
	for name, supported := range probe.WorksOn {
		if ok, _ := supported.(bool); ok {
			g.Platforms = append(g.Platforms, strings.ToLower(name))
		}
	}
	return g, nil
}

// --- content system ---

// Build is one downloadable build of a product.
type Build struct {
	BuildID       string `json:"buildId"`
	ProductID     string `json:"productId"`
	OS            string `json:"os"`
	Branch        string `json:"branch"`
	VersionName   string `json:"versionName"`
	Generation    int    `json:"generation"`
	DatePublished string `json:"datePublished"`
	Link          string `json:"link"` // fetchable build meta, pre-authorized
}

type buildList struct {
	Items []struct {
		BuildID     string  `json:"build_id"`
		ProductID   string  `json:"product_id"`
		OS          string  `json:"os"`
		Branch      *string `json:"branch"`
		VersionName string  `json:"version_name"`
		Generation  int     `json:"generation"`
		DatePublish string  `json:"date_published"`
		Link        string  `json:"link"`
	} `json:"items"`
}

// Builds lists downloadable builds, newest first. The answer is `{ items }` with
// snake_case fields and no depot information; the depots arrive through the build's
// link document.
func (c *Client) Builds(ctx context.Context, productID, os string) ([]Build, error) {
	if !ValidProductID(productID) {
		return nil, store.Configf("invalid GOG product id")
	}
	if os == "" {
		os = "windows"
	}
	endpoint := fmt.Sprintf("%s/products/%s/os/%s/builds?generation=2",
		c.cfg.ContentSystemURL, url.PathEscape(productID), url.PathEscape(os))
	body, err := c.contentGet(ctx, endpoint, "build list request failed")
	if err != nil {
		return nil, err
	}
	var parsed buildList
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, errors.New("gog: build list was not readable")
	}
	out := make([]Build, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		if item.Link == "" || item.BuildID == "" {
			continue
		}
		product := item.ProductID
		if product == "" {
			product = productID
		}
		out = append(out, Build{
			BuildID: item.BuildID, ProductID: product, OS: item.OS,
			Branch: derefString(item.Branch), VersionName: item.VersionName,
			Generation: item.Generation, DatePublished: item.DatePublish, Link: item.Link,
		})
	}
	return out, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (c *Client) contentGet(ctx context.Context, endpoint, failure string) ([]byte, error) {
	return c.accountGet(ctx, endpoint, failure)
}
