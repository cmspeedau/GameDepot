package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cmspeedau/gamedepot/internal/auth"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

// ---------- objects ----------

func (s *Server) objectPath(sha []byte) string {
	hexsha := hex.EncodeToString(sha)
	return filepath.Join(s.cfg.DataDir, "objects", hexsha[:2], hexsha)
}

func storeFile(dst string, src multipart.File, size int64) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	w := io.MultiWriter(f, h)
	if _, err := io.Copy(w, src); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	f.Close()
	sum := hex.EncodeToString(h.Sum(nil))
	if sum != filepath.Base(dst) {
		os.Remove(tmp)
		return "", fmt.Errorf("hash mismatch")
	}
	if err := os.Rename(tmp, dst); err != nil {
		// maybe already exists
		os.Remove(tmp)
		if _, err2 := os.Stat(dst); err2 != nil {
			return "", err
		}
	}
	return sum, nil
}

func (s *Server) handleObjectServe(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.Status(400)
		return
	}
	obj, err := s.db.GetObjectByID(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		c.Status(404)
		return
	}
	sha := obj.Sha256
	path := s.objectPath(sha)
	f, err := os.Open(path)
	if err != nil {
		c.Status(404)
		return
	}
	defer f.Close()

	etag := `"` + hex.EncodeToString(sha) + `"`
	if inm := c.GetHeader("If-None-Match"); inm != "" && inm == etag {
		c.Status(304)
		return
	}
	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Type", obj.ContentType)
	c.Status(200)
	io.Copy(c.Writer, f)
}

func (s *Server) handleObjectUpload(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.String(400, "missing file")
		return
	}
	defer file.Close()

	// compute hash first by reading into temp
	tmpDir := filepath.Join(s.cfg.DataDir, "objects")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		c.String(500, "mkdir error")
		return
	}
	tmp, err := os.CreateTemp(tmpDir, ".upload-*")
	if err != nil {
		c.String(500, "temp error")
		return
	}
	tmpPath := tmp.Name()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), file); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		c.String(500, "read error")
		return
	}
	tmp.Close()
	sum := h.Sum(nil)

	dst := s.objectPath(sum)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(tmpPath)
		c.String(500, "mkdir error")
		return
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		// maybe exists; remove tmp
		os.Remove(tmpPath)
		if _, err2 := os.Stat(dst); err2 != nil {
			c.String(500, "store error")
			return
		}
	}

	// content type
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}

	// upsert object row
	existing, err := s.db.GetObjectBySHA256(c.Request.Context(), sum)
	var objID uuid.UUID
	if err == nil {
		objID = existing.ID.Bytes
	} else {
		id := uuid.New()
		row, err := s.db.CreateObject(c.Request.Context(), db.CreateObjectParams{
			ID:          pgtype.UUID{Bytes: id, Valid: true},
			Sha256:      sum,
			Size:        header.Size,
			ContentType: ct,
		})
		if err != nil {
			// maybe race; try get
			if ex, err2 := s.db.GetObjectBySHA256(c.Request.Context(), sum); err2 == nil {
				objID = ex.ID.Bytes
			} else {
				c.String(500, "db error")
				return
			}
		} else {
			objID = row.ID.Bytes
		}
	}

	// redirect back with flash containing id
	c.Redirect(http.StatusFound, "/dashboard/objects?flash=uploaded:"+uuid.UUID(objID).String())
}

// ---------- games ----------

func (s *Server) handleGamesPage(c *gin.Context) {
	games, err := s.db.ListGames(c.Request.Context())
	if err != nil {
		c.String(500, "list games failed")
		return
	}
	s.render(c, "games.html", gin.H{"Title": "Games", "Games": games})
}

func (s *Server) handleGameNew(c *gin.Context) {
	companies, _ := s.db.ListCompanies(c.Request.Context())
	tags, _ := s.db.ListTags(c.Request.Context())
	s.render(c, "game_form.html", gin.H{"Title": "New game", "Companies": companies, "Tags": tags})
}

func (s *Server) handleGameCreate(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	slug := strings.TrimSpace(c.PostForm("slug"))
	summary := c.PostForm("summary")
	description := c.PostForm("description")
	if name == "" {
		c.Redirect(http.StatusFound, "/dashboard/games/new?flash=name+required")
		return
	}
	if slug == "" {
		slug = slugify(name)
	}
	id := uuid.New()
	var coverID pgtype.UUID
	if v := c.PostForm("cover_object_id"); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			coverID = pgtype.UUID{Bytes: u, Valid: true}
		}
	}
	_, err := s.db.CreateGame(c.Request.Context(), db.CreateGameParams{
		ID:            pgtype.UUID{Bytes: id, Valid: true},
		Slug:          slug,
		Name:          name,
		Summary:       summary,
		Description:   description,
		CoverObjectID: coverID,
	})
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/games/new?flash=create+failed")
		return
	}
	c.Redirect(http.StatusFound, "/dashboard/games/"+id.String()+"?flash=created")
}

func (s *Server) handleGamePage(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	game, err := s.db.GetGameByID(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		c.String(404, "not found")
		return
	}
	versions, _ := s.db.ListGameVersions(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	type versionLaunch struct {
		Version  db.GameVersion
		Launches []db.Launch
	}
	vls := make([]versionLaunch, 0, len(versions))
	for _, v := range versions {
		ls, _ := s.db.ListLaunchesForVersion(c.Request.Context(), v.ID)
		vls = append(vls, versionLaunch{Version: v, Launches: ls})
	}
	companies, _ := s.db.ListCompaniesForGame(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	tags, _ := s.db.ListTagsForGame(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	allCompanies, _ := s.db.ListCompanies(c.Request.Context())
	allTags, _ := s.db.ListTags(c.Request.Context())
	s.render(c, "game.html", gin.H{
		"Title":           game.Name,
		"Game":            game,
		"Versions":        versions,
		"VersionLaunches": vls,
		"Companies":       companies,
		"Tags":            tags,
		"AllCompanies":    allCompanies,
		"AllTags":         allTags,
	})
}

func (s *Server) handleGameVersions(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": gin.H{"code": "bad_request", "message": "bad id"}})
		return
	}
	versions, err := s.db.ListGameVersions(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		c.JSON(404, gin.H{"error": gin.H{"code": "not_found", "message": "game not found"}})
		return
	}
	type item struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Platform   string `json:"platform"`
		TotalSize  int64  `json:"totalSize"`
		ReleasedAt string `json:"releasedAt"`
	}
	out := make([]item, 0, len(versions))
	for _, v := range versions {
		out = append(out, item{
			ID:         uuidOf(v.ID),
			Name:       v.Name,
			Platform:   v.Platform,
			TotalSize:  v.TotalSize,
			ReleasedAt: timeOf(v.CreatedAt),
		})
	}
	c.JSON(200, out)
}

func (s *Server) handleGameUpdate(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	slug := strings.TrimSpace(c.PostForm("slug"))
	summary := c.PostForm("summary")
	description := c.PostForm("description")
	if slug == "" {
		slug = slugify(name)
	}
	var coverID pgtype.UUID
	if v := c.PostForm("cover_object_id"); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			coverID = pgtype.UUID{Bytes: u, Valid: true}
		}
	}
	_ = s.db.UpdateGame(c.Request.Context(), db.UpdateGameParams{
		ID:            pgtype.UUID{Bytes: id, Valid: true},
		Slug:          slug,
		Name:          name,
		Summary:       summary,
		Description:   description,
		CoverObjectID: coverID,
	})
	c.Redirect(http.StatusFound, "/dashboard/games/"+id.String()+"?flash=updated")
}

func (s *Server) handleGameDelete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	_ = s.db.DeleteGame(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	c.Redirect(http.StatusFound, "/dashboard/games?flash=deleted")
}

// ---------- companies & tags ----------

func (s *Server) handleCompaniesPage(c *gin.Context) {
	companies, _ := s.db.ListCompanies(c.Request.Context())
	s.render(c, "companies.html", gin.H{"Title": "Companies", "Companies": companies})
}

func (s *Server) handleCreateCompany(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.Redirect(http.StatusFound, "/dashboard/companies?flash=name+required")
		return
	}
	_, _ = s.db.CreateCompany(c.Request.Context(), db.CreateCompanyParams{
		ID:   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Name: name,
	})
	c.Redirect(http.StatusFound, "/dashboard/companies?flash=created")
}

func (s *Server) handleTagsPage(c *gin.Context) {
	tags, _ := s.db.ListTags(c.Request.Context())
	s.render(c, "tags.html", gin.H{"Title": "Tags", "Tags": tags})
}

func (s *Server) handleCreateTag(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.Redirect(http.StatusFound, "/dashboard/tags?flash=name+required")
		return
	}
	slug := slugify(name)
	_, _ = s.db.CreateTag(c.Request.Context(), db.CreateTagParams{
		ID:   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Slug: slug,
		Name: name,
	})
	c.Redirect(http.StatusFound, "/dashboard/tags?flash=created")
}

// ---------- versions ----------

func (s *Server) handleCreateVersion(c *gin.Context) {
	gameID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad game id")
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	platform := c.PostForm("platform")
	if name == "" || platform == "" {
		c.Redirect(http.StatusFound, "/dashboard/games/"+gameID.String()+"?flash=missing+fields")
		return
	}
	_, err = s.db.CreateGameVersion(c.Request.Context(), db.CreateGameVersionParams{
		ID:       pgtype.UUID{Bytes: uuid.New(), Valid: true},
		GameID:   pgtype.UUID{Bytes: gameID, Valid: true},
		Name:     name,
		Platform: platform,
	})
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/games/"+gameID.String()+"?flash=version+failed")
		return
	}
	c.Redirect(http.StatusFound, "/dashboard/games/"+gameID.String()+"?flash=version+created")
}

// ---------- collections ----------

func (s *Server) handleCollectionsPage(c *gin.Context) {
	sess := c.MustGet("session").(*auth.Session)
	cols, _ := s.db.ListCollectionsForUser(c.Request.Context(), pgtype.UUID{Bytes: sess.UserID, Valid: true})
	s.render(c, "collections.html", gin.H{"Title": "Collections", "Collections": cols})
}

func (s *Server) handleCreateCollection(c *gin.Context) {
	sess := c.MustGet("session").(*auth.Session)
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.Redirect(http.StatusFound, "/dashboard/collections?flash=name+required")
		return
	}
	_, _ = s.db.CreateCollection(c.Request.Context(), db.CreateCollectionParams{
		ID:     pgtype.UUID{Bytes: uuid.New(), Valid: true},
		UserID: pgtype.UUID{Bytes: sess.UserID, Valid: true},
		Name:   name,
	})
	c.Redirect(http.StatusFound, "/dashboard/collections?flash=created")
}

// ---------- client API ----------

func (s *Server) handleLibrary(c *gin.Context) {
	games, err := s.db.ListGames(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{"code": "internal", "message": "list failed"}})
		return
	}
	type item struct {
		ID               string  `json:"id"`
		Name             string  `json:"name"`
		Summary          string  `json:"summary"`
		CoverURL         *string `json:"coverUrl"`
		InstalledVersion *string `json:"installedVersionId"`
	}
	out := make([]item, 0, len(games))
	for _, g := range games {
		it := item{ID: uuid.UUID(g.ID.Bytes).String(), Name: g.Name, Summary: g.Summary}
		if g.CoverObjectID.Valid {
			u := "/api/v1/objects/" + uuid.UUID(g.CoverObjectID.Bytes).String()
			it.CoverURL = &u
		}
		out = append(out, it)
	}
	c.JSON(200, out)
}

func (s *Server) handleObjectsPage(c *gin.Context) {
	s.render(c, "objects.html", gin.H{"Title": "Objects"})
}
func (s *Server) handleGame(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": gin.H{"code": "bad_request", "message": "bad id"}})
		return
	}
	g, err := s.db.GetGameByID(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		c.JSON(404, gin.H{"error": gin.H{"code": "not_found", "message": "game not found"}})
		return
	}
	c.JSON(200, g)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			b.WriteRune('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}
