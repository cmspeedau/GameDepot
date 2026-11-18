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
	"github.com/cmspeedau/gamedepot/internal/store/gog"
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
		"Title":        "Store connections",
		"Connections":  views,
		"AuthorizeURL": gog.AuthorizeURL(),
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
	sealedAccess, err := s.secrets.Seal(tokens.AccessToken)
	if err != nil {
		return err
	}
	sealedRefresh := ""
	if tokens.RefreshToken != "" {
		if sealedRefresh, err = s.secrets.Seal(tokens.RefreshToken); err != nil {
			return err
		}
	}
	_, err = s.db.CreateStoreConnection(ctx, db.CreateStoreConnectionParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Provider:     "gog",
		AccountLabel: pgtype.Text{String: "GOG account", Valid: true},
		AccountID:    pgtype.Text{String: tokens.AccountID, Valid: tokens.AccountID != ""},
		AccessToken:  sealedAccess,
		RefreshToken: pgtype.Text{String: sealedRefresh, Valid: sealedRefresh != ""},
		ExpiresAt:    pgtype.Timestamptz{Time: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second), Valid: true},
		Status:       "active",
	})
	return err
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

// gogClient returns a usable GOG client for the instance's newest active connection,
// refreshing the stored token when it is close to expiry. Providers rotate refresh
// tokens on use, so a refresh is written straight back.
func (s *Server) gogClient(ctx context.Context) (*gog.Client, db.StoreConnection, bool) {
	conn, err := s.db.NewestActiveStoreConnection(ctx, "gog")
	if err != nil {
		return nil, db.StoreConnection{}, false
	}
	access, err := s.secrets.Open(conn.AccessToken)
	if err != nil {
		slog.WarnContext(ctx, "gog access token undecryptable; the credential key changed?", "err", err)
		s.markStoreConnectionError(ctx, "gog")
		return nil, db.StoreConnection{}, false
	}
	refresh := ""
	if conn.RefreshToken.Valid {
		if refresh, err = s.secrets.Open(conn.RefreshToken.String); err != nil {
			slog.WarnContext(ctx, "gog refresh token undecryptable", "err", err)
			s.markStoreConnectionError(ctx, "gog")
			return nil, db.StoreConnection{}, false
		}
	}

	client := gog.NewClient(s.gogConfig(), gog.Tokens{AccessToken: access, RefreshToken: refresh})
	if conn.ExpiresAt.Valid && time.Until(conn.ExpiresAt.Time) < 10*time.Minute && refresh != "" {
		tokens, err := client.Refresh(ctx)
		if err != nil {
			slog.WarnContext(ctx, "gog token refresh failed", "err", err)
			if errors.Is(err, store.ErrReauth) {
				s.markStoreConnectionError(ctx, "gog")
				return nil, db.StoreConnection{}, false
			}
			// A transient provider failure is not a dead credential: keep using the
			// access token, which may still be valid for a while.
		} else {
			if sealed, err := s.secrets.Seal(tokens.AccessToken); err == nil {
				sealedRefresh := ""
				if tokens.RefreshToken != "" {
					sealedRefresh, _ = s.secrets.Seal(tokens.RefreshToken)
				}
				if err := s.db.UpdateStoreConnectionToken(ctx, db.UpdateStoreConnectionTokenParams{
					ID:           conn.ID,
					AccessToken:  sealed,
					RefreshToken: pgtype.Text{String: sealedRefresh, Valid: sealedRefresh != ""},
					ExpiresAt:    pgtype.Timestamptz{Time: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second), Valid: true},
				}); err != nil {
					slog.WarnContext(ctx, "rotated gog token not saved", "err", err)
				}
			}
			client = gog.NewClient(s.gogConfig(), tokens)
		}
	}
	return client, conn, true
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
