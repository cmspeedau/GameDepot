package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cmspeedau/gamedepot/internal/auth"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/ingest"
	"github.com/cmspeedau/gamedepot/internal/store"
	"github.com/cmspeedau/gamedepot/internal/store/epic"
	"github.com/cmspeedau/gamedepot/internal/store/gog"
	"github.com/cmspeedau/gamedepot/internal/store/ubisoft"
	"github.com/cmspeedau/gamedepot/internal/tasks"
)

// storeView is what the dashboard shows about one connection: never a credential.
type storeView struct {
	ID          string
	Provider    string
	Account     string
	Status      string
	NeedsReauth bool
	ExpiresAt   string
	UpdatedAt   string
}

// storesReady reports whether the store surface can work: enabled by config and
// backed by a usable credential key. The dashboard uses it to hide the entry point
// rather than link to a page that would only explain why it is unavailable.
func (s *Server) storesReady() bool {
	return s.cfg.StoreEnabled && s.secrets.Enabled()
}

func (s *Server) storeEnabled(c *gin.Context) bool {
	if !s.cfg.StoreEnabled {
		c.String(http.StatusNotFound, "store features are disabled; set GAMEDEPOT_STORE=true")
		return false
	}
	if !s.secrets.Enabled() {
		c.String(http.StatusServiceUnavailable, "GAMEDEPOT_CREDENTIAL_KEY is not set, so store credentials cannot be stored")
		return false
	}
	return true
}

func maskConnection(row db.StoreConnection) storeView {
	view := storeView{
		ID:        uuidOf(row.ID),
		Provider:  row.Provider,
		Account:   textOr(row.AccountLabel),
		Status:    row.Status,
		UpdatedAt: row.UpdatedAt.Time.Format("2006-01-02 15:04"),
	}
	if view.Account == "" {
		view.Account = textOr(row.AccountID)
	}
	if row.ExpiresAt.Valid {
		view.ExpiresAt = row.ExpiresAt.Time.Format("2006-01-02 15:04")
	}
	view.NeedsReauth = row.Status == "error"
	return view
}

// handleStoresPage lists store connections and, for GOG, the entitled library with an
// ingest action per title.
func (s *Server) handleStoresPage(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	rows, err := s.db.ListStoreConnections(c.Request.Context())
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "store connections list", "err", err)
	}
	views := make([]storeView, 0, len(rows))
	for _, row := range rows {
		views = append(views, maskConnection(row))
	}
	s.render(c, "stores.html", gin.H{
		"Title":            "Store connections",
		"Connections":      views,
		"AuthorizeURL":     gog.AuthorizeURL(),
		"EpicAuthorizeURL": epic.AuthorizeURL(),
	})
}

// handleGogConnect exchanges a pasted sign-in code for a session and stores it sealed.
func (s *Server) handleGogConnect(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	code := c.PostForm("code")
	if strings.TrimSpace(code) == "" {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=paste+the+code")
		return
	}
	tokens, err := gog.ExchangeCodeWithConfig(c.Request.Context(), s.gogConfig(), code)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "gog connect failed", "err", err)
		c.Redirect(http.StatusFound, "/dashboard/stores?flash="+urlEscape(flashOf(err)))
		return
	}
	if err := s.saveGogConnection(c.Request.Context(), tokens); err != nil {
		slog.ErrorContext(c.Request.Context(), "gog connection save", "err", err)
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=save+failed")
		return
	}
	c.Redirect(http.StatusFound, "/dashboard/stores?flash=connected")
}

func (s *Server) saveGogConnection(ctx context.Context, tokens gog.Tokens) error {
	return s.saveProviderConnection(ctx, "gog", "GOG account", tokens.AccountID,
		tokens.AccessToken, tokens.RefreshToken, time.Duration(tokens.ExpiresIn)*time.Second)
}

// handleEpicConnect exchanges a pasted code from Epic's redirect page for a session.
func (s *Server) handleEpicConnect(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	code := c.PostForm("code")
	if strings.TrimSpace(code) == "" {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=paste+the+code")
		return
	}
	tokens, err := epic.ExchangeCode(c.Request.Context(), s.epicConfig(), code)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "epic connect failed", "err", err)
		c.Redirect(http.StatusFound, "/dashboard/stores?flash="+urlEscape(flashOf(err)))
		return
	}
	label := "Epic account"
	if err := s.saveProviderConnection(c.Request.Context(), "epic", label, tokens.AccountID,
		tokens.AccessToken, tokens.RefreshToken, time.Duration(tokens.ExpiresIn)*time.Second); err != nil {
		slog.ErrorContext(c.Request.Context(), "epic connection save", "err", err)
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=save+failed")
		return
	}
	c.Redirect(http.StatusFound, "/dashboard/stores?flash=connected")
}

// handleUbisoftConnect authenticates with email+password and stores the session.
func (s *Server) handleUbisoftConnect(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")
	if email == "" || password == "" {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=email+and+password+required")
		return
	}
	res, err := ubisoft.CreateUbisoftSession(c.Request.Context(), ubisoft.DefaultConfig(), email, password)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "ubisoft connect failed", "err", err)
		c.Redirect(http.StatusFound, "/dashboard/stores?flash="+urlEscape(flashOf(err)))
		return
	}
	if res.TwoFactor != nil {
		// Stash the 2FA ticket in a short-lived cookie-less flow is out of scope;
		// surface it as a flash for now.
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=2fa+required")
		return
	}
	sess := res.Session
	expires := 24 * time.Hour
	if err := s.saveProviderConnection(c.Request.Context(), "ubisoft", "Ubisoft account", sess.UserID,
		sess.Ticket, sess.RememberMeTicket, expires); err != nil {
		slog.ErrorContext(c.Request.Context(), "ubisoft connection save", "err", err)
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=save+failed")
		return
	}
	c.Redirect(http.StatusFound, "/dashboard/stores?flash=connected")
}

// handleUbisoftLibrary renders the entitled games as an HTMX fragment.
func (s *Server) handleUbisoftLibrary(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	client, _, ok := s.ubisoftClient(c.Request.Context())
	if !ok {
		s.render(c, "stores_library.html", gin.H{"Error": "No Ubisoft connection. Connect an account first."})
		return
	}
	games, err := client.Library(c.Request.Context())
	if err != nil {
		msg := "Library fetch failed"
		if errors.Is(err, store.ErrReauth) {
			msg = "Ubisoft rejected the stored credential; connect the account again."
			s.markStoreConnectionError(c.Request.Context(), "ubisoft")
		}
		s.render(c, "stores_library.html", gin.H{"Error": msg + ": " + err.Error()})
		return
	}
	seen := map[string]bool{}
	items := make([]store.Game, 0, len(games))
	for _, g := range games {
		if seen[g.ID] {
			continue
		}
		seen[g.ID] = true
		items = append(items, g)
	}
	s.render(c, "stores_library.html", gin.H{"Games": items})
}

// handleUbisoftIngest queues one product for acquisition.
func (s *Server) handleUbisoftIngest(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	sess := c.MustGet("session").(*auth.Session)
	productID := strings.TrimSpace(c.PostForm("product_id"))
	if productID == "" {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=invalid+product+id")
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	platform := strings.TrimSpace(c.PostForm("platform"))
	if platform == "" {
		platform = "windows"
	}

	client, conn, ok := s.ubisoftClient(c.Request.Context())
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=no+ubisoft+connection")
		return
	}

	task := s.tasks.StartFor("ingest", "ubisoft:"+productID+":"+platform, sess.UserID,
		func(ctx context.Context, t *tasks.Task) error {
			t.AppendLog(fmt.Sprintf("acquiring Ubisoft product %s (%s)", productID, platform))
			result, err := s.ingestSvc.Run(ctx, client, ingest.Options{
				Provider:    "ubisoft",
				ProductID:   productID,
				Title:       title,
				Platform:    platform,
				StagingRoot: s.stagingRoot(),
				DepotRoot:   s.depotRoot(),
				PublisherID: pgtype.UUID{Bytes: sess.UserID, Valid: true},
				Force:       c.PostForm("force") == "1",
				Progress: func(done, total int64) {
					if total > 0 {
						t.SetProgress(float64(done) / float64(total))
					}
				},
			})
			if err != nil {
				if errors.Is(err, store.ErrReauth) {
					s.markStoreConnectionError(ctx, "ubisoft")
				}
				return err
			}
			if result.Reused {
				t.AppendLog("already current at build " + result.BuildID)
				return nil
			}
			t.AppendLog(fmt.Sprintf("published %s (%d files, %d bytes)", result.Digest, result.Files, result.Bytes))
			t.AppendLog("game: /dashboard/games/" + result.GameID.String())
			_ = conn
			return nil
		})
	c.Redirect(http.StatusFound, "/dashboard/tasks/"+task.ID.String())
}

// ubisoftClient returns a usable Ubisoft client for the instance's newest active connection.
func (s *Server) ubisoftClient(ctx context.Context) (*ubisoft.Client, db.StoreConnection, bool) {
	sess, ok := s.providerSession(ctx, "ubisoft")
	if !ok {
		return nil, db.StoreConnection{}, false
	}
	client := ubisoft.NewClient(ubisoft.DefaultConfig(), ubisoft.Tokens{Ticket: sess.Access, RememberMeTicket: sess.Refresh})
	return client, sess.Conn, true
}

// no active connection exists. It is the single dispatch point the store update check
// uses so adding a provider means extending one switch.
func (s *Server) providerClient(ctx context.Context, provider string) (store.Provider, db.StoreConnection, bool) {
	switch provider {
	case "gog":
		c, conn, ok := s.gogClient(ctx)
		if !ok {
			return nil, db.StoreConnection{}, false
		}
		return c, conn, true
	case "epic":
		c, conn, ok := s.epicClient(ctx)
		if !ok {
			return nil, db.StoreConnection{}, false
		}
		return c, conn, true
	case "ubisoft":
		c, conn, ok := s.ubisoftClient(ctx)
		if !ok {
			return nil, db.StoreConnection{}, false
		}
		return c, conn, true
	default:
		return nil, db.StoreConnection{}, false
	}
}

// StoreUpdateReport summarizes one store update check pass.
type StoreUpdateReport struct {
	Checked  int
	Outdated []ingest.StoreProductStatus
	Errors   int
}

// RunStoreUpdateCheck compares every recorded store product against the build the
// provider serves now and reports which are outdated. It never downloads; ingest is
// still triggered explicitly by an operator or a later automation step.
func (s *Server) RunStoreUpdateCheck(ctx context.Context) (StoreUpdateReport, error) {
	report := StoreUpdateReport{}
	if !s.storesReady() {
		return report, nil
	}
	products, err := s.db.ListAllStoreProducts(ctx)
	if err != nil {
		return report, fmt.Errorf("store update check: list products: %w", err)
	}
	byProvider := map[string][]db.StoreProduct{}
	for _, p := range products {
		byProvider[p.Provider] = append(byProvider[p.Provider], p)
	}
	for provider, ps := range byProvider {
		client, _, ok := s.providerClient(ctx, provider)
		if !ok {
			slog.WarnContext(ctx, "store update check: no active connection", "provider", provider)
			report.Errors += len(ps)
			continue
		}
		statuses := ingest.CheckStoreProducts(ctx, client, ps)
		for _, st := range statuses {
			report.Checked++
			if st.Err != nil {
				report.Errors++
				slog.WarnContext(ctx, "store update check: build lookup failed",
					"provider", st.Provider, "product", st.ProductID, "platform", st.Platform, "err", st.Err)
				continue
			}
			if st.Outdated {
				report.Outdated = append(report.Outdated, st)
				slog.InfoContext(ctx, "store update check: update available",
					"provider", st.Provider, "product", st.ProductID, "title", st.Title,
					"platform", st.Platform, "current", st.CurrentBuild, "latest", st.LatestBuild)
			}
		}
	}
	slog.InfoContext(ctx, "store update check complete",
		"checked", report.Checked, "outdated", len(report.Outdated), "errors", report.Errors)
	return report, nil
}

// handleEpicLibrary lists the account's entitled products. Epic builds are now acquired
// through the same ingest path as GOG: the binary install manifest is downloaded and
// decoded before the install tree is staged, and chunk downloads stream from Epic's CDN.
func (s *Server) handleEpicLibrary(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	client, _, ok := s.epicClient(c.Request.Context())
	if !ok {
		s.render(c, "stores_epic.html", gin.H{"Error": "No Epic connection. Connect an account first."})
		return
	}
	games, err := client.Library(c.Request.Context())
	if err != nil {
		if errors.Is(err, store.ErrReauth) {
			s.markStoreConnectionError(c.Request.Context(), "epic")
		}
		s.render(c, "stores_epic.html", gin.H{"Error": "Epic library request failed: " + err.Error()})
		return
	}
	s.render(c, "stores_epic.html", gin.H{"Games": games})
}

// handleStoreDisconnect removes a stored connection. Tokens are dropped, not just
// marked: a revoked credential should not survive in the database.
func (s *Server) handleStoreDisconnect(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=bad+id")
		return
	}
	if err := s.db.DeleteStoreConnection(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true}); err != nil {
		slog.ErrorContext(c.Request.Context(), "store connection delete", "err", err)
	}
	c.Redirect(http.StatusFound, "/dashboard/stores?flash=disconnected")
}

// handleGogLibrary renders the entitled games as an HTMX fragment.
func (s *Server) handleGogLibrary(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	client, _, ok := s.gogClient(c.Request.Context())
	if !ok {
		s.render(c, "stores_library.html", gin.H{"Error": "No GOG connection. Connect an account first."})
		return
	}
	games, err := client.Library(c.Request.Context())
	if err != nil {
		msg := "Library fetch failed"
		if errors.Is(err, store.ErrReauth) {
			msg = "GOG rejected the stored credential; connect the account again."
			s.markStoreConnectionError(c.Request.Context(), "gog")
		}
		s.render(c, "stores_library.html", gin.H{"Error": msg + ": " + err.Error()})
		return
	}
	seen := map[string]bool{}
	items := make([]store.Game, 0, len(games))
	for _, g := range games {
		if seen[g.ID] {
			continue
		}
		seen[g.ID] = true
		items = append(items, g)
	}
	s.render(c, "stores_library.html", gin.H{"Games": items})
}

// handleGogIngest queues one product for acquisition. The task row is the job record:
// progress, log and failure state are visible on the task page like any other job.
func (s *Server) handleGogIngest(c *gin.Context) {
	if !s.storeEnabled(c) {
		return
	}
	sess := c.MustGet("session").(*auth.Session)
	productID := strings.TrimSpace(c.PostForm("product_id"))
	if !gog.ValidProductID(productID) {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=invalid+product+id")
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	platform := strings.TrimSpace(c.PostForm("platform"))
	if platform == "" {
		platform = "windows"
	}
	// Reject an unacquirable platform before starting a task that can only fail, so
	// the operator reads the reason in the flash instead of opening a dead job.
	if _, err := gog.ContentOS(platform); err != nil {
		slog.InfoContext(c.Request.Context(), "gog ingest refused", "product", productID, "platform", platform, "err", err)
		c.Redirect(http.StatusFound, "/dashboard/stores?flash="+urlEscape(flashOf(err)))
		return
	}

	client, conn, ok := s.gogClient(c.Request.Context())
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/stores?flash=no+gog+connection")
		return
	}

	task := s.tasks.StartFor("ingest", "gog:"+productID+":"+platform, sess.UserID,
		func(ctx context.Context, t *tasks.Task) error {
			t.AppendLog(fmt.Sprintf("acquiring GOG product %s (%s)", productID, platform))
			result, err := s.ingestSvc.Run(ctx, client, ingest.Options{
				Provider:    "gog",
				ProductID:   productID,
				Title:       title,
				Platform:    platform,
				StagingRoot: s.stagingRoot(),
				DepotRoot:   s.depotRoot(),
				PublisherID: pgtype.UUID{Bytes: sess.UserID, Valid: true},
				Force:       c.PostForm("force") == "1",
				Progress: func(done, total int64) {
					if total > 0 {
						t.SetProgress(float64(done) / float64(total))
					}
				},
			})
			if err != nil {
				if errors.Is(err, store.ErrReauth) {
					s.markStoreConnectionError(ctx, "gog")
				}
				return err
			}
			if result.Reused {
				t.AppendLog("already current at build " + result.BuildID)
				return nil
			}
			t.AppendLog(fmt.Sprintf("published %s (%d files, %d bytes)", result.Digest, result.Files, result.Bytes))
			t.AppendLog("game: /dashboard/games/" + result.GameID.String())
			_ = conn // the connection is refreshed inside gogClient; nothing else to record
			return nil
		})
	c.Redirect(http.StatusFound, "/dashboard/tasks/"+task.ID.String())
}

// gogConfig points the provider at this deployment's settings.
func (s *Server) gogConfig() gog.Config {
	cfg := gog.DefaultConfig()
	cfg.Timeout = 2 * time.Minute // chunk fetches are large
	return cfg
}

// session is one provider's decrypted credentials plus its stored row.
type session struct {
	Conn    db.StoreConnection
	Access  string
	Refresh string
}

// renewedTokens is what a provider hands back after rotating its pair.
type renewedTokens struct {
	Access    string
	Refresh   string
	ExpiresIn time.Duration
}

// providerSession loads the newest active connection for a provider and opens its
// sealed credentials. A value that will not decrypt means the deployment key changed:
// the connection is flagged rather than treated as plaintext.
func (s *Server) providerSession(ctx context.Context, provider string) (session, bool) {
	conn, err := s.db.NewestActiveStoreConnection(ctx, provider)
	if err != nil {
		return session{}, false
	}
	access, err := s.secrets.Open(conn.AccessToken)
	if err != nil {
		slog.WarnContext(ctx, "stored access token undecryptable; the credential key changed?",
			"provider", provider, "err", err)
		s.markStoreConnectionError(ctx, provider)
		return session{}, false
	}
	out := session{Conn: conn, Access: access}
	if conn.RefreshToken.Valid {
		refresh, err := s.secrets.Open(conn.RefreshToken.String)
		if err != nil {
			slog.WarnContext(ctx, "stored refresh token undecryptable", "provider", provider, "err", err)
			s.markStoreConnectionError(ctx, provider)
			return session{}, false
		}
		out.Refresh = refresh
	}
	return out, true
}

// renewIfExpiring renews a session whose access token is close to expiry and persists
// the rotated pair. Providers rotate refresh tokens on use, so writing both values back
// is not optional: a dropped rotation breaks the account next time.
//
// A transient provider failure is deliberately not treated as a dead credential; the
// existing access token may still work, and retrying a rate-limited endpoint in a loop
// is worse than letting the next call fail visibly.
func (s *Server) renewIfExpiring(ctx context.Context, sess session, renew func(ctx context.Context) (renewedTokens, error)) (renewedTokens, bool, bool) {
	needsRefresh := sess.Conn.ExpiresAt.Valid && time.Until(sess.Conn.ExpiresAt.Time) < 10*time.Minute && sess.Refresh != ""
	if !needsRefresh {
		return renewedTokens{}, false, false
	}
	tokens, err := renew(ctx)
	if err != nil {
		slog.WarnContext(ctx, "token refresh failed", "provider", sess.Conn.Provider, "err", err)
		if errors.Is(err, store.ErrReauth) {
			s.markStoreConnectionError(ctx, sess.Conn.Provider)
			return renewedTokens{}, true, false
		}
		return renewedTokens{}, false, false
	}
	sealed, err := s.secrets.Seal(tokens.Access)
	if err != nil {
		slog.WarnContext(ctx, "rotated token could not be sealed", "err", err)
		return tokens, false, true
	}
	sealedRefresh := ""
	if tokens.Refresh != "" {
		sealedRefresh, _ = s.secrets.Seal(tokens.Refresh)
	}
	if err := s.db.UpdateStoreConnectionToken(ctx, db.UpdateStoreConnectionTokenParams{
		ID:           sess.Conn.ID,
		AccessToken:  sealed,
		RefreshToken: pgtype.Text{String: sealedRefresh, Valid: sealedRefresh != ""},
		ExpiresAt:    pgtype.Timestamptz{Time: time.Now().Add(tokens.ExpiresIn), Valid: tokens.ExpiresIn > 0},
	}); err != nil {
		slog.WarnContext(ctx, "rotated token not saved", "provider", sess.Conn.Provider, "err", err)
	}
	return tokens, false, true
}

// gogClient returns a usable GOG client for the instance's newest active connection.
func (s *Server) gogClient(ctx context.Context) (*gog.Client, db.StoreConnection, bool) {
	sess, ok := s.providerSession(ctx, "gog")
	if !ok {
		return nil, db.StoreConnection{}, false
	}
	cfg := s.gogConfig()
	client := gog.NewClient(cfg, gog.Tokens{AccessToken: sess.Access, RefreshToken: sess.Refresh})
	if tokens, abort, renewed := s.renewIfExpiring(ctx, sess, func(ctx context.Context) (renewedTokens, error) {
		fresh, err := client.Refresh(ctx)
		return gogTokens(fresh, err)
	}); abort {
		return nil, db.StoreConnection{}, false
	} else if renewed {
		client = gog.NewClient(cfg, gog.Tokens{AccessToken: tokens.Access, RefreshToken: tokens.Refresh, ExpiresIn: int(tokens.ExpiresIn.Seconds())})
	}
	return client, sess.Conn, true
}

func gogTokens(t gog.Tokens, err error) (renewedTokens, error) {
	if err != nil {
		return renewedTokens{}, err
	}
	return renewedTokens{
		Access:    t.AccessToken,
		Refresh:   t.RefreshToken,
		ExpiresIn: time.Duration(t.ExpiresIn) * time.Second,
	}, nil
}

// epicClient returns a usable Epic client for the instance's newest active connection.
func (s *Server) epicClient(ctx context.Context) (*epic.Client, db.StoreConnection, bool) {
	sess, ok := s.providerSession(ctx, "epic")
	if !ok {
		return nil, db.StoreConnection{}, false
	}
	cfg := s.epicConfig()
	client := epic.NewClient(cfg, epic.Tokens{AccessToken: sess.Access, RefreshToken: sess.Refresh})
	if tokens, abort, renewed := s.renewIfExpiring(ctx, sess, func(ctx context.Context) (renewedTokens, error) {
		fresh, err := client.Refresh(ctx)
		return epicTokens(fresh, err)
	}); abort {
		return nil, db.StoreConnection{}, false
	} else if renewed {
		client = epic.NewClient(cfg, epic.Tokens{AccessToken: tokens.Access, RefreshToken: tokens.Refresh, ExpiresIn: int(tokens.ExpiresIn.Seconds())})
	}
	return client, sess.Conn, true
}

func epicTokens(t epic.Tokens, err error) (renewedTokens, error) {
	if err != nil {
		return renewedTokens{}, err
	}
	return renewedTokens{
		Access:    t.AccessToken,
		Refresh:   t.RefreshToken,
		ExpiresIn: time.Duration(t.ExpiresIn) * time.Second,
	}, nil
}

func (s *Server) epicConfig() epic.Config {
	cfg := epic.DefaultConfig()
	cfg.Timeout = 2 * time.Minute
	return cfg
}

// saveProviderConnection stores a freshly exchanged token pair, sealed. A reconnect
// replaces the previous row for the same account rather than accumulating connections.
func (s *Server) saveProviderConnection(ctx context.Context, provider, label, accountID string, access, refresh string, expiresIn time.Duration) error {
	sealedAccess, err := s.secrets.Seal(access)
	if err != nil {
		return err
	}
	sealedRefresh := ""
	if refresh != "" {
		if sealedRefresh, err = s.secrets.Seal(refresh); err != nil {
			return err
		}
	}
	_, err = s.db.CreateStoreConnection(ctx, db.CreateStoreConnectionParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Provider:     provider,
		AccountLabel: pgtype.Text{String: label, Valid: label != ""},
		AccountID:    pgtype.Text{String: accountID, Valid: accountID != ""},
		AccessToken:  sealedAccess,
		RefreshToken: pgtype.Text{String: sealedRefresh, Valid: sealedRefresh != ""},
		ExpiresAt:    pgtype.Timestamptz{Time: time.Now().Add(expiresIn), Valid: expiresIn > 0},
		Status:       "active",
	})
	return err
}

func (s *Server) markStoreConnectionError(ctx context.Context, provider string) {
	conn, err := s.db.NewestActiveStoreConnection(ctx, provider)
	if err != nil {
		return
	}
	if err := s.db.SetStoreConnectionStatus(ctx, db.SetStoreConnectionStatusParams{
		ID: conn.ID, Status: "error",
	}); err != nil {
		slog.ErrorContext(ctx, "store connection status update", "err", err)
	}
}

func flashOf(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i > 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	if len(msg) > 90 {
		msg = msg[:90]
	}
	return strings.ReplaceAll(msg, " ", "+")
}

func urlEscape(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23").Replace(s)
}

func textOr(t pgtype.Text) string {
	if t.Valid {
		return t.String
	}
	return ""
}
