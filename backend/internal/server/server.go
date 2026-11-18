package server

import (
	"encoding/base64"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cmspeedau/gamedepot/internal/auth"
	"github.com/cmspeedau/gamedepot/internal/config"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/ingest"
	"github.com/cmspeedau/gamedepot/internal/secrets"
	"github.com/cmspeedau/gamedepot/internal/tasks"
	"github.com/cmspeedau/gamedepot/internal/version"
	"github.com/cmspeedau/gamedepot/web"
)

type Server struct {
	cfg       config.Config
	pool      *pgxpool.Pool
	db        *db.Queries
	sessions  *auth.SessionManager
	limiter   *auth.RateLimiter
	tasks     *tasks.Registry
	secrets   *secrets.Box
	ingestSvc *ingest.Service
	templates *template.Template
	static    fs.FS
}

func New(cfg config.Config, pool *pgxpool.Pool) *Server {
	s := &Server{cfg: cfg, pool: pool, db: db.New(pool), sessions: auth.NewSessionManager(pool), limiter: auth.NewRateLimiter(), tasks: tasks.New()}

	// The sealing key is optional for the rest of the server; store routes refuse to
	// run without it. config.Load has already rejected a malformed key and a store
	// that is enabled without one, so a parse failure here means the value disappeared
	// between validation and use, which is reported rather than swallowed.
	if cfg.CredentialKey != "" {
		box, err := secrets.NewBox(cfg.CredentialKey)
		if err != nil {
			slog.Error("credential key", "err", err)
		} else {
			s.secrets = box
		}
	}
	s.ingestSvc = ingest.NewService(s.db, s.stagingRoot(), s.depotRoot())

	tmpl, err := template.New("").Funcs(template.FuncMap{
		"uuid": func(u pgtype.UUID) string {
			if !u.Valid {
				return ""
			}
			return uuid.UUID(u.Bytes).String()
		},
		"pct": func(f float64) int { return int(f * 100) },
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		panic(err)
	}
	s.templates = tmpl

	sub, err := fs.Sub(web.Assets, "static")
	if err != nil {
		panic(err)
	}
	s.static = sub

	return s
}

func (s *Server) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(RequestID())
	r.Use(AccessLog())

	// Static assets.
	r.StaticFS("/static", http.FS(s.static))

	// 404/405 pages.
	r.NoRoute(s.handleNotFound)
	r.NoMethod(s.handleNotFound)

	// Health. Liveness is cheap and does not touch the database; readiness probes
	// the database and is what a load balancer or orchestrator should watch.
	r.GET("/api/v1/health", s.handleHealth)
	r.GET("/api/v1/ready", s.handleReady)

	// Setup and auth.
	r.GET("/setup", s.handleSetupPage)
	r.POST("/setup", s.handleSetupSubmit)
	r.GET("/signin", s.handleSigninPage)
	r.POST("/signin", s.handleSigninSubmit)
	r.POST("/signout", s.handleSignout)

	// Signup via invitation.
	r.GET("/signup", s.handleSignupPage)
	r.POST("/signup", s.handleSignupSubmit)

	// Dashboard (session required).
	dash := r.Group("/dashboard")
	dash.Use(s.requireSessionMiddleware(), s.csrfProtect())
	dash.GET("", s.handleDashboard)
	dash.GET("/users", s.handleUsersPage)
	dash.POST("/users/:id/disable", s.handleUserDisable)
	dash.POST("/users/:id/password", s.handleUserPassword)
	dash.GET("/invitations", s.handleInvitationsPage)
	dash.POST("/invitations", s.handleCreateInvitation)
	dash.GET("/sessions", s.handleSessionsPage)
	dash.POST("/sessions/:id/revoke", s.handleRevokeSession)
	dash.GET("/games", s.handleGamesPage)
	dash.GET("/games/new", s.handleGameNew)
	dash.POST("/games", s.handleGameCreate)
	dash.GET("/games/:id", s.handleGamePage)
	dash.POST("/games/:id", s.handleGameUpdate)
	dash.POST("/games/:id/delete", s.handleGameDelete)
	dash.POST("/games/:id/versions", s.handleCreateVersion)
	dash.POST("/versions/:id/launches", s.handleCreateLaunch)
	dash.POST("/launches/:id/delete", s.handleDeleteLaunch)
	dash.GET("/companies", s.handleCompaniesPage)
	dash.POST("/companies", s.handleCreateCompany)
	dash.GET("/tags", s.handleTagsPage)
	dash.POST("/tags", s.handleCreateTag)
	dash.GET("/collections", s.handleCollectionsPage)
	dash.POST("/collections", s.handleCreateCollection)
	dash.GET("/clients", s.handleClientsPage)
	dash.POST("/clients/approve", s.handleApproveClient)
	dash.POST("/clients/:id/revoke", s.handleRevokeClient)
	dash.GET("/libraries", s.handleLibrariesPage)
	dash.POST("/libraries", s.handleCreateLibrary)
	dash.POST("/libraries/:id/delete", s.handleDeleteLibrary)
	dash.GET("/releases/new", s.handlePublishPage)
	dash.POST("/releases", s.handlePublishSubmit)
	dash.GET("/tasks/:id", s.handleTaskPage)
	dash.POST("/releases/:digest/verify", s.handleVerifyRelease)
	dash.POST("/objects", s.handleObjectUpload)

	// Store accounts and ingestion (gated on GAMEDEPOT_STORE + a credential key).
	dash.GET("/stores", s.handleStoresPage)
	dash.POST("/stores/gog/connect", s.handleGogConnect)
	dash.GET("/stores/gog/library", s.handleGogLibrary)
	dash.POST("/stores/gog/ingest", s.handleGogIngest)
	dash.POST("/stores/connection/:id/disconnect", s.handleStoreDisconnect)

	// Client API (JSON).
	api := r.Group("/api/v1")
	api.GET("/version", s.handleVersion)
	api.POST("/pairing/initiate", s.handlePairingInitiate)
	api.POST("/pairing/claim", s.handlePairingClaim)
	api.GET("/me", s.requireBearer(s.handleMe))
	api.GET("/objects/:id", s.requireBearer(s.handleObjectServe))
	api.GET("/library", s.requireBearer(s.handleLibrary))
	api.GET("/games/:id", s.requireBearer(s.handleGame))
	api.GET("/games/:id/versions", s.requireBearer(s.handleGameVersions))
	api.GET("/games/:id/recipe", s.requireBearer(s.handleGameRecipe))
	api.GET("/releases/:versionId", s.requireBearer(s.handleRelease))
	api.GET("/playtime", s.requireBearer(s.handlePlaytimeList))
	api.POST("/playtime", s.requireBearer(s.handlePlaytimeAdd))
	api.GET("/depot/manifests/:digest", s.requireBearer(s.handleDepotManifest))
	api.GET("/depot/objects/:sha256", s.requireBearer(s.handleDepotObject))

	// Cloud saves (require saves:read / saves:write scope on the client token).
	api.GET("/saves", s.requireBearer(s.handleSavesList))
	api.GET("/saves/manifests/:gameId", s.requireBearer(s.handleSaveManifest))
	api.POST("/saves/slot/:gameId/:slot/push", s.requireBearer(s.handleSavePush))
	api.GET("/saves/slot/:gameId/:slot/download", s.requireBearer(s.handleSaveDownload))
	api.GET("/saves/slot/:gameId/:slot/versions", s.requireBearer(s.handleSaveVersions))
	api.POST("/saves/slot/:gameId/:slot/versions/:versionId/restore", s.requireBearer(s.handleSaveRestore))
	api.POST("/saves/slot/:gameId/:slot/versions/:versionId/pin", s.requireBearer(s.handleSavePin))
	api.DELETE("/saves/slot/:gameId/:slot/versions/:versionId", s.requireBearer(s.handleSaveDeleteVersion))

	return r
}

func (s *Server) handleHealth(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// handleVersion reports the server build version for upgrade verification.
func (s *Server) handleVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": version.Version})
}

// handleReady reports readiness: the process is up and the database answers.
func (s *Server) handleReady(c *gin.Context) {
	if err := s.pool.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "unavailable", "message": "database unreachable"}})
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) render(c *gin.Context, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	if _, ok := data["Flash"]; !ok {
		data["Flash"] = c.Query("flash")
	}
	if sess, ok := c.Get("session"); ok {
		if ss, ok := sess.(*auth.Session); ok {
			data["CSRF"] = base64.RawURLEncoding.EncodeToString(ss.CSRF)
		}
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(c.Writer, name, data); err != nil {
		slogError(c, err)
		c.Status(500)
	}
}

func (s *Server) handleNotFound(c *gin.Context) {
	c.Status(404)
	s.render(c, "error.html", gin.H{"Title": "Not found"})
}

func (s *Server) requireSession(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(auth.SessionCookie)
		if err != nil || token == "" {
			c.Redirect(302, "/signin")
			c.Abort()
			return
		}
		sess, err := s.sessions.Get(c.Request.Context(), token)
		if err != nil {
			auth.ClearSessionCookie(c.Writer)
			c.Redirect(302, "/signin")
			c.Abort()
			return
		}
		if sess.ExpiresAt.Before(time.Now()) {
			_ = s.sessions.Delete(c.Request.Context(), token)
			auth.ClearSessionCookie(c.Writer)
			c.Redirect(302, "/signin")
			c.Abort()
			return
		}
		c.Set("session", sess)
		c.Set("token", token)
		next(c)
	}
}

func (s *Server) handleSetupPage(c *gin.Context) {
	count, err := s.db.CountUsers(c.Request.Context())
	if err != nil {
		c.String(500, "database error")
		return
	}
	if count > 0 {
		c.Redirect(302, "/signin")
		return
	}
	if st, err := s.db.GetSetting(c.Request.Context(), "setup_complete"); err == nil && len(st.Value) > 0 {
		c.Redirect(302, "/signin")
		return
	}
	s.render(c, "setup.html", gin.H{"Title": "Setup", "Token": c.Query("token")})
}

func (s *Server) handleSetupSubmit(c *gin.Context) {
	if !s.limiter.Allow(c.ClientIP(), 5, 15*time.Minute) {
		s.render(c, "setup.html", gin.H{"Title": "Setup", "Error": "Too many attempts. Try again later."})
		return
	}
	count, err := s.db.CountUsers(c.Request.Context())
	if err != nil {
		c.String(500, "database error")
		return
	}
	if count > 0 {
		c.Redirect(302, "/signin")
		return
	}

	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	token := strings.TrimSpace(c.PostForm("token"))
	if username == "" || len(password) < 8 {
		s.render(c, "setup.html", gin.H{"Title": "Setup", "Token": token, "Error": "Username required and password at least 8 characters."})
		return
	}

	stored, err := auth.SetupTokenStored(c.Request.Context(), s.pool)
	if err != nil || !auth.VerifySetupToken(stored, token) {
		s.render(c, "setup.html", gin.H{"Title": "Setup", "Token": token, "Error": "Invalid or expired setup token."})
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		c.String(500, "hash error")
		return
	}

	id := uuid.New()
	_, err = s.db.CreateUser(c.Request.Context(), db.CreateUserParams{
		ID:           pgtype.UUID{Bytes: id, Valid: true},
		Username:     username,
		DisplayName:  username,
		PasswordHash: hash,
		Admin:        true,
	})
	if err != nil {
		s.render(c, "setup.html", gin.H{"Title": "Setup", "Token": token, "Error": "Could not create admin. Username may be taken."})
		return
	}

	if err := auth.CompleteSetup(c.Request.Context(), s.pool); err != nil {
		c.String(500, "setup finalize error")
		return
	}

	sessionToken, err := s.sessions.Create(c.Request.Context(), id, true, c.Request.UserAgent())
	if err != nil {
		c.String(500, "session error")
		return
	}
	auth.SetSessionCookie(c.Writer, sessionToken, auth.SecureCookie(s.cfg.ExternalURL), int(auth.SessionLifetime.Seconds()))
	c.Redirect(302, "/dashboard")
}

func (s *Server) handleSigninPage(c *gin.Context) {
	count, err := s.db.CountUsers(c.Request.Context())
	if err != nil {
		c.String(500, "database error")
		return
	}
	if count == 0 {
		c.Redirect(302, "/setup")
		return
	}
	s.render(c, "signin.html", gin.H{"Title": "Sign in"})
}

func (s *Server) handleSigninSubmit(c *gin.Context) {
	if !s.limiter.Allow(c.ClientIP(), 10, 15*time.Minute) {
		s.render(c, "signin.html", gin.H{"Title": "Sign in", "Error": "Too many attempts. Try again later."})
		return
	}
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	remember := c.PostForm("remember") == "on"

	user, err := s.db.GetUserByUsername(c.Request.Context(), username)
	if err != nil {
		s.render(c, "signin.html", gin.H{"Title": "Sign in", "Error": "Invalid username or password."})
		return
	}
	if user.Disabled {
		s.render(c, "signin.html", gin.H{"Title": "Sign in", "Error": "Account disabled."})
		return
	}
	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		s.render(c, "signin.html", gin.H{"Title": "Sign in", "Error": "Invalid username or password."})
		return
	}

	token, err := s.sessions.Create(c.Request.Context(), user.ID.Bytes, remember, c.Request.UserAgent())
	if err != nil {
		c.String(500, "session error")
		return
	}
	auth.SetSessionCookie(c.Writer, token, auth.SecureCookie(s.cfg.ExternalURL), int(auth.SessionLifetime.Seconds()))
	if remember {
		auth.SetSessionCookie(c.Writer, token, auth.SecureCookie(s.cfg.ExternalURL), int(auth.RememberLifetime.Seconds()))
	}
	c.Redirect(302, "/dashboard")
}

func (s *Server) handleSignout(c *gin.Context) {
	if token, err := c.Cookie(auth.SessionCookie); err == nil && token != "" {
		_ = s.sessions.Delete(c.Request.Context(), token)
	}
	auth.ClearSessionCookie(c.Writer)
	c.Redirect(302, "/signin")
}

func (s *Server) handleDashboard(c *gin.Context) {
	sess := c.MustGet("session").(*auth.Session)
	user, err := s.db.GetUserByID(c.Request.Context(), pgtype.UUID{Bytes: sess.UserID, Valid: true})
	if err != nil {
		c.String(500, "user lookup failed")
		return
	}
	s.render(c, "dashboard.html", gin.H{
		"Title":         "Dashboard",
		"Username":      user.Username,
		"StoresEnabled": s.storesReady(),
	})
}

func slogError(c *gin.Context, err error) {
	_ = c
	_ = err
}
