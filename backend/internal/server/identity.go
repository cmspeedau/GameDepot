package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cmspeedau/gamedepot/internal/auth"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

// ---------- middleware ----------

func (s *Server) requireSessionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(auth.SessionCookie)
		if err != nil || token == "" {
			c.Redirect(http.StatusFound, "/signin")
			c.Abort()
			return
		}
		sess, err := s.sessions.Get(c.Request.Context(), token)
		if err != nil || sess.ExpiresAt.Before(time.Now()) {
			auth.ClearSessionCookie(c.Writer)
			c.Redirect(http.StatusFound, "/signin")
			c.Abort()
			return
		}
		c.Set("session", sess)
		c.Set("token", token)
		c.Next()
	}
}

func (s *Server) csrfProtect() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		v, ok := c.Get("session")
		if !ok {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		sess := v.(*auth.Session)
		if !auth.CSRFMatch(sess, c.PostForm("_csrf")) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func (s *Server) requireBearer(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "unauthorized", "message": "missing bearer token"}})
			c.Abort()
			return
		}
		client, err := s.db.GetClientByTokenHash(c.Request.Context(), auth.TokenHash(parts[1]))
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "unauthorized", "message": "invalid token"}})
			c.Abort()
			return
		}
		if client.RevokedAt.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "unauthorized", "message": "token revoked"}})
			c.Abort()
			return
		}
		c.Set("client", client)
		_ = s.db.TouchClient(c.Request.Context(), client.ID)
		next(c)
	}
}

// ---------- signup ----------

func (s *Server) handleSignupPage(c *gin.Context) {
	count, _ := s.db.CountUsers(c.Request.Context())
	if count == 0 {
		c.Redirect(http.StatusFound, "/setup")
		return
	}
	s.render(c, "signup.html", gin.H{"Title": "Sign up", "Code": c.Query("code")})
}

func (s *Server) handleSignupSubmit(c *gin.Context) {
	code := strings.TrimSpace(c.PostForm("code"))
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")

	if code == "" || username == "" || len(password) < 8 {
		s.render(c, "signup.html", gin.H{"Title": "Sign up", "Code": code, "Error": "All fields required; password at least 8 characters."})
		return
	}

	inv, err := s.db.GetInvitationByCodeHash(c.Request.Context(), auth.TokenHash(code))
	if err != nil || inv.UsedAt.Valid || inv.ExpiresAt.Time.Before(time.Now()) {
		s.render(c, "signup.html", gin.H{"Title": "Sign up", "Code": code, "Error": "Invalid or expired invitation."})
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
		Admin:        false,
	})
	if err != nil {
		s.render(c, "signup.html", gin.H{"Title": "Sign up", "Code": code, "Error": "Could not create user. Username may be taken."})
		return
	}

	_ = s.db.MarkInvitationUsed(c.Request.Context(), db.MarkInvitationUsedParams{
		ID:     inv.ID,
		UsedBy: pgtype.UUID{Bytes: id, Valid: true},
	})

	token, err := s.sessions.Create(c.Request.Context(), id, false, c.Request.UserAgent())
	if err != nil {
		c.String(500, "session error")
		return
	}
	auth.SetSessionCookie(c.Writer, token, auth.SecureCookie(s.cfg.ExternalURL), int(auth.SessionLifetime.Seconds()))
	c.Redirect(http.StatusFound, "/dashboard")
}

// ---------- users ----------

func (s *Server) handleUsersPage(c *gin.Context) {
	users, err := s.db.ListUsers(c.Request.Context())
	if err != nil {
		c.String(500, "list users failed")
		return
	}
	s.render(c, "users.html", gin.H{"Title": "Users", "Users": users})
}

func (s *Server) handleUserDisable(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	disabled := c.PostForm("disabled") == "true"
	_ = s.db.SetUserDisabled(c.Request.Context(), db.SetUserDisabledParams{
		ID:       pgtype.UUID{Bytes: id, Valid: true},
		Disabled: disabled,
	})
	c.Redirect(http.StatusFound, "/dashboard/users?flash=updated")
}

func (s *Server) handleUserPassword(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	password := c.PostForm("password")
	if len(password) < 8 {
		c.Redirect(http.StatusFound, "/dashboard/users?flash=password+too+short")
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		c.String(500, "hash error")
		return
	}
	_ = s.db.UpdateUserPassword(c.Request.Context(), db.UpdateUserPasswordParams{
		ID:           pgtype.UUID{Bytes: id, Valid: true},
		PasswordHash:  hash,
	})
	c.Redirect(http.StatusFound, "/dashboard/users?flash=password+reset")
}

// ---------- invitations ----------

func (s *Server) handleInvitationsPage(c *gin.Context) {
	invs, err := s.db.ListInvitations(c.Request.Context())
	if err != nil {
		c.String(500, "list invitations failed")
		return
	}
	s.render(c, "invitations.html", gin.H{"Title": "Invitations", "Invitations": invs})
}

func (s *Server) handleCreateInvitation(c *gin.Context) {
	sess := c.MustGet("session").(*auth.Session)
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		c.String(500, "rand error")
		return
	}
	code := base64.RawURLEncoding.EncodeToString(b)
	expires := time.Now().Add(7 * 24 * time.Hour)
	_, err := s.db.CreateInvitation(c.Request.Context(), db.CreateInvitationParams{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		CodeHash:  auth.TokenHash(code),
		CreatedBy: pgtype.UUID{Bytes: sess.UserID, Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		c.String(500, "create invitation failed")
		return
	}
	s.render(c, "invitations.html", gin.H{
		"Title": "Invitations",
		"Flash": "Invitation created. Code (shown once): " + code,
	})
}

// ---------- sessions ----------

func (s *Server) handleSessionsPage(c *gin.Context) {
	sess := c.MustGet("session").(*auth.Session)
	// list all sessions for simplicity in v1 scaffold
	rows, err := s.db.ListSessionsForUser(c.Request.Context(), pgtype.UUID{Bytes: sess.UserID, Valid: true})
	if err != nil {
		c.String(500, "list sessions failed")
		return
	}
	s.render(c, "sessions.html", gin.H{"Title": "Sessions", "Sessions": rows, "CurrentID": sess.ID})
}

func (s *Server) handleRevokeSession(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	_ = s.db.DeleteSession(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	c.Redirect(http.StatusFound, "/dashboard/sessions?flash=revoked")
}

// ---------- clients / pairing ----------

func (s *Server) handleClientsPage(c *gin.Context) {
	clients, _ := s.db.ListAllClients(c.Request.Context())
	pending, _ := s.db.ListPendingPairingCodes(c.Request.Context())
	s.render(c, "clients.html", gin.H{"Title": "Clients", "Clients": clients, "Pending": pending})
}

func (s *Server) handleApproveClient(c *gin.Context) {
	code := strings.TrimSpace(c.PostForm("code"))
	sess := c.MustGet("session").(*auth.Session)
	pc, err := s.db.GetPairingCode(c.Request.Context(), auth.TokenHash(code))
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/clients?flash=invalid+code")
		return
	}
	if pc.ApprovedAt.Valid || pc.ClaimedAt.Valid || pc.ExpiresAt.Time.Before(time.Now()) {
		c.Redirect(http.StatusFound, "/dashboard/clients?flash=code+unavailable")
		return
	}
	res, err := s.db.ApprovePairingCode(c.Request.Context(), db.ApprovePairingCodeParams{
		CodeHash:   pc.CodeHash,
		ApprovedBy: pgtype.UUID{Bytes: sess.UserID, Valid: true},
	})
	if err != nil || res == 0 {
		c.Redirect(http.StatusFound, "/dashboard/clients?flash=approve+failed")
		return
	}
	c.Redirect(http.StatusFound, "/dashboard/clients?flash=approved")
}

func (s *Server) handleRevokeClient(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	_ = s.db.RevokeClient(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	c.Redirect(http.StatusFound, "/dashboard/clients?flash=revoked")
}
func (s *Server) handlePairingInitiate(c *gin.Context) {
	if !s.limiter.Allow(c.ClientIP(), 20, 15*time.Minute) {
		c.JSON(429, gin.H{"error": gin.H{"code": "rate_limited", "message": "too many attempts"}})
		return
	}
	var body struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "name required"}})
		return
	}
	if body.Platform == "" {
		body.Platform = "linux"
	}
	// generate code
	tb := make([]byte, 4)
	if _, err := rand.Read(tb); err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "internal", "message": "rand failed"}})
		return
	}
	code := strings.ToUpper(hex.EncodeToString(tb))
	expires := time.Now().Add(10 * time.Minute)
	scopes := []string{"library:read", "playtime:write", "collections:write"}
	_ = s.db.CreatePairingCode(c.Request.Context(), db.CreatePairingCodeParams{
		CodeHash:  auth.TokenHash(code),
		Name:      body.Name,
		Platform:  body.Platform,
		Scopes:    scopes,
		ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	})
	c.JSON(http.StatusOK, gin.H{"code": code, "expiresAt": expires.UTC().Format(time.RFC3339Nano)})
}

func (s *Server) handlePairingClaim(c *gin.Context) {
	var body struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "code required"}})
		return
	}
	pc, err := s.db.GetPairingCode(c.Request.Context(), auth.TokenHash(strings.ToUpper(strings.TrimSpace(body.Code))))
	if err != nil {
		c.JSON(http.StatusGone, gin.H{"error": gin.H{"code": "gone", "message": "invalid or expired code"}})
		return
	}
	if !pc.ApprovedAt.Valid {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "conflict", "message": "not yet approved"}})
		return
	}
	if pc.ClaimedAt.Valid {
		c.JSON(http.StatusGone, gin.H{"error": gin.H{"code": "gone", "message": "already claimed"}})
		return
	}
	if pc.ExpiresAt.Time.Before(time.Now()) {
		c.JSON(http.StatusGone, gin.H{"error": gin.H{"code": "gone", "message": "expired"}})
		return
	}

	// generate client token
	tb := make([]byte, 32)
	if _, err := rand.Read(tb); err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "internal", "message": "rand failed"}})
		return
	}
	token := base64.RawURLEncoding.EncodeToString(tb)
	clientID := uuid.New()
	_, err = s.db.CreateClient(c.Request.Context(), db.CreateClientParams{
		ID:        pgtype.UUID{Bytes: clientID, Valid: true},
		UserID:    pc.ApprovedBy,
		Name:      pc.Name,
		Platform:  pc.Platform,
		TokenHash: auth.TokenHash(token),
		Scopes:    pc.Scopes,
	})
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "internal", "message": "create client failed"}})
		return
	}
	res, err := s.db.ClaimPairingCode(c.Request.Context(), db.ClaimPairingCodeParams{
		CodeHash: pc.CodeHash,
		ClientID: pgtype.UUID{Bytes: clientID, Valid: true},
	})
	if err != nil || res == 0 {
		c.JSON(500, gin.H{"error": gin.H{"code": "internal", "message": "claim failed"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"clientId": clientID.String(), "token": token})
}

func (s *Server) handleMe(c *gin.Context) {
	client := c.MustGet("client").(db.Client)
	user, err := s.db.GetUserByID(c.Request.Context(), client.UserID)
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "internal", "message": "user lookup failed"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": uuid.UUID(user.ID.Bytes).String(), "username": user.Username, "displayName": user.DisplayName})
}

// ---------- helpers ----------

func base32Encode(b []byte) string {
	// Crockford-ish uppercase hex-ish: use base32 without padding, uppercase
	const alphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
	out := make([]byte, 0, 8)
	var buf uint64
	var bits int
	for _, by := range b {
		buf = (buf << 8) | uint64(by)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, alphabet[(buf>>bits)&31])
		}
	}
	if bits > 0 {
		out = append(out, alphabet[(buf<<(5-bits))&31])
	}
	if len(out) > 8 {
		out = out[:8]
	}
	for len(out) < 8 {
		out = append(out, 'A')
	}
	return string(out)
}
