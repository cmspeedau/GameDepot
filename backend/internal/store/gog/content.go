package gog

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5" //nolint:gosec // GOG declares chunk digests as MD5; this is the provider's integrity field, not a security boundary
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/cmspeedau/gamedepot/internal/store"
)

// gogGalaxyPath renders a hash the way GOG prefixes content paths: a1/b2/a1b2...
// Used for both depot manifests and chunk objects.
func gogGalaxyPath(hash string) string {
	if strings.Contains(hash, "/") {
		return hash
	}
	if len(hash) < 4 {
		return hash
	}
	return fmt.Sprintf("%s/%s/%s", hash[:2], hash[2:4], hash)
}

// getZlibJSON reads a body that GOG serves zlib-compressed with no Content-Encoding
// header, falling back to plain JSON. A tolerant decode is required because the same
// route has answered both ways across products.
func getZlibJSON(body []byte, failure string) (map[string]any, error) {
	if reader, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
		defer reader.Close()
		plain, err := io.ReadAll(io.LimitReader(reader, 64<<20))
		if err == nil {
			var out map[string]any
			if json.Unmarshal(plain, &out) == nil {
				return out, nil
			}
		}
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("gog: %s", failure)
	}
	return out, nil
}

// Depot is one downloadable content set inside a build. The v2 depot object carries no
// operating system field of its own (verified against live builds): the build request is
// already scoped by OS, so filtering depots by platform would be inventing a field.
//
// Note the link document also contains GOG's own clientId/clientSecret keys. They are
// deliberately not parsed, and must never be logged.
type Depot struct {
	Manifest       string   `json:"manifest"`
	ProductID      string   `json:"productId"`
	Size           int64    `json:"size"`
	CompressedSize int64    `json:"compressedSize"`
	Languages      []string `json:"languages"`
	OSBitness      []string `json:"osBitness"`
	IsGogDepot     bool     `json:"isGogDepot"`
}

// BuildMeta is a build's link document.
type BuildMeta struct {
	BuildID          string
	BaseProductID    string
	InstallDirectory string
	// Platform is what GOG labels this build: "windows" or "osx". It is checked
	// against the requested platform, because a vocabulary slip in the path segment
	// would otherwise deliver the wrong operating system's files quietly.
	Platform string
	Depots   []Depot
}

// BuildMeta fetches the zlib-compressed build meta named by a build's link. The link is
// pre-authorized; the token rides along harmlessly.
func (c *Client) BuildMeta(ctx context.Context, link string) (BuildMeta, error) {
	if link == "" {
		return BuildMeta{}, store.Configf("a GOG build link is required")
	}
	if _, err := url.ParseRequestURI(link); err != nil {
		return BuildMeta{}, store.Configf("the GOG build link is not a valid URL")
	}
	body, err := c.accountGet(ctx, link, "build meta request failed")
	if err != nil {
		return BuildMeta{}, err
	}
	meta, err := getZlibJSON(body, "the GOG build meta was not readable")
	if err != nil {
		return BuildMeta{}, err
	}
	out := BuildMeta{
		BuildID:          jsonString(meta["buildId"]),
		BaseProductID:    jsonString(meta["baseProductId"]),
		InstallDirectory: jsonString(meta["installDirectory"]),
		Platform:         jsonString(meta["platform"]),
	}
	if depots, ok := meta["depots"].([]any); ok {
		for _, raw := range depots {
			buf, _ := json.Marshal(raw)
			var d Depot
			if json.Unmarshal(buf, &d) == nil && d.Manifest != "" {
				out.Depots = append(out.Depots, d)
			}
		}
	}
	return out, nil
}

func jsonString(v any) string {
	s, _ := v.(string)
	return s
}

// Chunk is one addressable piece of a depot file. Both digests are declared by GOG:
// CompressedMD5 over the wire bytes, MD5 over the inflated bytes.
type Chunk struct {
	CompressedMD5  string `json:"compressedMd5"`
	CompressedSize int64  `json:"compressedSize"`
	MD5            string `json:"md5"`
	Size           int64  `json:"size"`
}

// DepotFile is one file entry in a depot manifest.
type DepotFile struct {
	Path    string
	Size    int64
	MD5     string
	SHA256  string
	Chunks  []Chunk
	Flags   []string
	Executa bool // GOG flags an executable file with "executable"
}

// SecureEndpoint is one signed download endpoint template for a product.
type SecureEndpoint struct {
	EndpointName string            `json:"endpoint_name"`
	URLFormat    string            `json:"url_format"`
	Parameters   map[string]string `json:"parameters"`
	Priority     int               `json:"priority"`
	FallbackOnly bool              `json:"fallback_only"`
}

// DepotManifest fetches one depot's file list from the content CDN. The meta path is
// open: no credentials are sent.
func (c *Client) DepotManifest(ctx context.Context, manifestHash string) ([]DepotFile, error) {
	if !safeHash(manifestHash) {
		return nil, store.Configf("invalid GOG depot manifest hash")
	}
	endpoint := fmt.Sprintf("%s/content-system/v2/meta/%s", c.cfg.CDNURL, gogGalaxyPath(manifestHash))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gog: depot manifest request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gog: depot manifest %s failed (%d)", manifestHash, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, errors.New("gog: depot manifest response unreadable")
	}
	payload, err := getZlibJSON(body, fmt.Sprintf("GOG depot manifest %s was not readable", manifestHash))
	if err != nil {
		return nil, err
	}
	var files []DepotFile
	depot, _ := payload["depot"].(map[string]any)
	items, _ := depot["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if jsonString(item["type"]) != "DepotFile" {
			continue // DepotDirectory and DepotLink carry no bytes
		}
		path := jsonString(item["path"])
		if path == "" {
			continue
		}
		f := DepotFile{
			Path:   normalizeDepotPath(path),
			MD5:    jsonString(item["md5"]),
			SHA256: jsonString(item["sha256"]),
		}
		if flags, ok := item["flags"].([]any); ok {
			for _, fl := range flags {
				s := jsonString(fl)
				f.Flags = append(f.Flags, s)
				if s == "executable" {
					f.Executa = true
				}
			}
		}
		if chunks, ok := item["chunks"].([]any); ok {
			for _, cr := range chunks {
				buf, _ := json.Marshal(cr)
				var ch Chunk
				if json.Unmarshal(buf, &ch) == nil {
					f.Chunks = append(f.Chunks, ch)
					f.Size += ch.Size
				}
			}
		}
		files = append(files, f)
	}
	return files, nil
}

var hashPattern = regexp.MustCompile(`^[0-9a-fA-F]{8,64}$`)

func safeHash(h string) bool { return hashPattern.MatchString(h) }

// normalizeDepotPath converts a Windows-authored depot path to the logical POSIX form
// the depot store uses, and drops any leading separators.
func normalizeDepotPath(path string) string {
	return strings.TrimLeft(strings.ReplaceAll(path, "\\", "/"), "/")
}

// unsafeTemplateChars rejects a returned URL format that carries whitespace or control
// characters. Braces are expected: they mark the parameters substituted per chunk.
var unsafeTemplateChars = regexp.MustCompile(`[\s\x00-\x1f]`)

// SecureLinks signs the chunk-download endpoints for a product. Every chunk URL must
// come from here; the older direct paths 404. Tokens are time-limited, so an ingest
// that stalls may need a fresh signing pass.
func (c *Client) SecureLinks(ctx context.Context, productID string) ([]SecureEndpoint, error) {
	if !ValidProductID(productID) {
		return nil, store.Configf("invalid GOG product id")
	}
	endpoint := fmt.Sprintf("%s/products/%s/secure_link?_version=2&generation=2&path=/",
		c.cfg.ContentSystemURL, url.PathEscape(productID))
	body, err := c.accountGet(ctx, endpoint, "secure link request failed")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		URLs []SecureEndpoint `json:"urls"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, errors.New("gog: secure link response was not readable")
	}
	out := make([]SecureEndpoint, 0, len(parsed.URLs))
	for _, e := range parsed.URLs {
		if e.URLFormat == "" || unsafeTemplateChars.MatchString(e.URLFormat) {
			continue
		}
		// The substituted URL is checked where it is used; a template is not a URL yet.
		if e.Parameters == nil {
			e.Parameters = map[string]string{}
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errors.New("gog: GOG returned no download endpoints for this product")
	}
	sortEndpoints(out)
	return out, nil
}

func sortEndpoints(out []SecureEndpoint) {
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Priority < out[j-1].Priority; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
}

// BuildChunkURL fills one endpoint template for a chunk. The client appends the chunk's
// galaxy path to the endpoint's path parameter before substituting, exactly as the
// launcher does.
func BuildChunkURL(endpoint SecureEndpoint, galaxySuffix string) string {
	out := endpoint.URLFormat
	for key, value := range endpoint.Parameters {
		// An empty value is meaningful: the path parameter starts blank and gains
		// only the chunk's galaxy path.
		replacement := value
		if key == "path" {
			replacement = strings.TrimSuffix(value, "/") + "/" + galaxySuffix
		}
		out = strings.ReplaceAll(out, "{"+key+"}", replacement)
	}
	return out
}

const chunkAttempts = 5

// acquireChunk fetches one chunk and returns its uncompressed bytes.
//
// Both declared digests are verified: the compressed MD5 over the wire bytes and the
// plain MD5 over the inflated ones. A mismatch fails the acquisition rather than
// writing suspect bytes. Endpoints rotate across attempts, mirroring the launcher.
func (c *Client) acquireChunk(ctx context.Context, endpoints []SecureEndpoint, filePath string, chunk Chunk) ([]byte, error) {
	key := chunk.CompressedMD5
	if key == "" {
		key = chunk.MD5
	}
	if key == "" {
		return nil, fmt.Errorf("gog: chunk without an MD5 in %s", filePath)
	}
	if !safeHash(key) {
		return nil, store.Configf("invalid GOG chunk hash in %s", filePath)
	}
	galaxy := gogGalaxyPath(key)
	last := "no attempt"
	for attempt := 0; attempt < chunkAttempts && len(endpoints) > 0; attempt++ {
		endpoint := endpoints[min(attempt, len(endpoints)-1)]
		body, status, err := c.getBytes(ctx, BuildChunkURL(endpoint, galaxy))
		if err != nil {
			last = "connection failed"
			continue
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			// The signed link is rejected, not the session: a retry with the same
			// token cannot help, so surface it for a requeue.
			return nil, fmt.Errorf("gog: the download secure link for chunk %s was rejected (%d); re-run the ingest to sign a fresh one", key, status)
		}
		if status != http.StatusOK {
			last = fmt.Sprintf("HTTP %d", status)
			continue
		}
		if sum := md5Hex(body); !strings.EqualFold(sum, key) {
			last = fmt.Sprintf("compressed MD5 mismatch (got %s)", sum)
			continue
		}
		plain, err := inflate(body)
		if err != nil {
			return nil, fmt.Errorf("gog: chunk %s for %s is not a readable zlib stream", key, filePath)
		}
		if chunk.MD5 != "" && !strings.EqualFold(md5Hex(plain), chunk.MD5) {
			return nil, fmt.Errorf("gog: chunk MD5 mismatch for %s (%s)", filePath, key)
		}
		return plain, nil
	}
	return nil, fmt.Errorf("gog: failed to fetch chunk %s for %s: %s", key, filePath, last)
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // provider-declared digest, see the note on the import
	return hex.EncodeToString(sum[:])
}

func inflate(body []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, 1<<30))
}

func (c *Client) getBytes(ctx context.Context, endpoint string) ([]byte, int, error) {
	// The endpoint is assembled from provider-supplied templates, so the final URL is
	// checked here rather than trusting the pieces.
	u, err := url.ParseRequestURI(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, 0, store.Configf("GOG returned an unusable download URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
