package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cmspeedau/gamedepot/internal/auth"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/depot"
	"github.com/cmspeedau/gamedepot/internal/tasks"
)

func (s *Server) depotRoot() string {
	return filepath.Join(s.cfg.DataDir, "depot")
}

// stagingRoot holds the temporary tree a store ingest assembles before chunking it into
// the depot. It is scratch space: contents are removed when a run finishes, including a
// failed one, and the scheduled cleanup may sweep leftovers older than a day.
func (s *Server) stagingRoot() string {
	return filepath.Join(s.cfg.DataDir, "staging")
}

// ---------- libraries ----------

func (s *Server) handleLibrariesPage(c *gin.Context) {
	libs, _ := s.db.ListLibraries(c.Request.Context())
	s.render(c, "libraries.html", gin.H{"Title": "Libraries", "Libraries": libs})
}

func (s *Server) handleCreateLibrary(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	path := strings.TrimSpace(c.PostForm("path"))
	if name == "" || path == "" {
		c.Redirect(http.StatusFound, "/dashboard/libraries?flash=missing+fields")
		return
	}
	_, _ = s.db.CreateLibrary(c.Request.Context(), db.CreateLibraryParams{
		ID:   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Name: name,
		Path: path,
		Kind: "directory",
	})
	c.Redirect(http.StatusFound, "/dashboard/libraries?flash=created")
}

func (s *Server) handleDeleteLibrary(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	_ = s.db.DeleteLibrary(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	c.Redirect(http.StatusFound, "/dashboard/libraries?flash=deleted")
}

// ---------- publish ----------

func (s *Server) handlePublishPage(c *gin.Context) {
	games, _ := s.db.ListGames(c.Request.Context())
	libs, _ := s.db.ListLibraries(c.Request.Context())
	s.render(c, "publish.html", gin.H{"Title": "Publish release", "Games": games, "Libraries": libs})
}

func (s *Server) handlePublishSubmit(c *gin.Context) {
	sess := c.MustGet("session").(*auth.Session)
	gameID, err := uuid.Parse(c.PostForm("game_id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/releases/new?flash=bad+game")
		return
	}
	versionID, err := uuid.Parse(c.PostForm("version_id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/releases/new?flash=bad+version")
		return
	}
	sourceDir := strings.TrimSpace(c.PostForm("source_dir"))
	if sourceDir == "" {
		c.Redirect(http.StatusFound, "/dashboard/releases/new?flash=missing+source")
		return
	}

	task := s.tasks.Start("publish", sess.UserID, func(ctx context.Context, t *tasks.Task) error {
		t.AppendLog("walking source: " + sourceDir)
		digest, err := depot.BuildRelease(sourceDir, s.depotRoot(), func(done, total int64) {
			if total > 0 {
				t.SetProgress(float64(done) / float64(total))
			}
		})
		if err != nil {
			return err
		}
		t.AppendLog("published digest: " + digest)

		// read manifest for total size
		manifestBytes, err := os.ReadFile(filepath.Join(s.depotRoot(), "manifests", digest+".json"))
		if err != nil {
			return err
		}
		var m depot.Manifest
		if err := json.Unmarshal(manifestBytes, &m); err != nil {
			return err
		}

		_, err = s.db.CreateRelease(ctx, db.CreateReleaseParams{
			ID:          pgtype.UUID{Bytes: uuid.New(), Valid: true},
			GameID:      pgtype.UUID{Bytes: gameID, Valid: true},
			VersionID:   pgtype.UUID{Bytes: versionID, Valid: true},
			Digest:      digest,
			Manifest:    manifestBytes,
			TotalSize:   m.TotalSize,
			PublishedBy: pgtype.UUID{Bytes: sess.UserID, Valid: true},
		})
		if err != nil {
			return err
		}
		t.AppendLog("release row created")
		return nil
	})

	c.Redirect(http.StatusFound, "/dashboard/tasks/"+task.ID.String())
}

// ---------- tasks ----------

func (s *Server) handleTaskPage(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(400, "bad id")
		return
	}
	task, ok := s.tasks.Get(id)
	if !ok {
		c.String(404, "task not found")
		return
	}
	view := task.View()
	s.render(c, "task.html", gin.H{"Title": "Task", "Task": view})
}

// ---------- verify ----------

func (s *Server) handleVerifyRelease(c *gin.Context) {
	digest := c.Param("digest")
	rel, err := s.db.GetReleaseByDigest(c.Request.Context(), digest)
	if err != nil {
		c.String(404, "release not found")
		return
	}
	var m depot.Manifest
	if err := json.Unmarshal(rel.Manifest, &m); err != nil {
		c.String(500, "bad manifest")
		return
	}
	if err := depot.VerifyRelease(s.depotRoot(), m); err != nil {
		c.Redirect(http.StatusFound, "/dashboard/releases?flash=verify+failed:+%22"+err.Error()+"%22")
		return
	}
	c.Redirect(http.StatusFound, "/dashboard/releases?flash=verified")
}

// ---------- client API: releases & depot ----------

func (s *Server) handleRelease(c *gin.Context) {
	versionID, err := uuid.Parse(c.Param("versionId"))
	if err != nil {
		c.JSON(400, gin.H{"error": gin.H{"code": "bad_request", "message": "bad id"}})
		return
	}
	// find game_id from version? need game id. Look up version first via db? No GetGameVersionByID? We have GetGameVersion (by id).
	gv, err := s.db.GetGameVersion(c.Request.Context(), pgtype.UUID{Bytes: versionID, Valid: true})
	if err != nil {
		c.JSON(404, gin.H{"error": gin.H{"code": "not_found", "message": "version not found"}})
		return
	}
	rel, err := s.db.GetReleaseByGameVersion(c.Request.Context(), db.GetReleaseByGameVersionParams{
		GameID:    gv.GameID,
		VersionID: pgtype.UUID{Bytes: versionID, Valid: true},
	})
	if err != nil {
		c.JSON(404, gin.H{"error": gin.H{"code": "not_found", "message": "release not found"}})
		return
	}
	launches, err := s.db.ListLaunchesForVersion(c.Request.Context(), pgtype.UUID{Bytes: versionID, Valid: true})
	if err != nil {
		launches = nil
	}
	launchDTOs := make([]launchDTO, 0, len(launches))
	for _, l := range launches {
		launchDTOs = append(launchDTOs, launchDTO{
			ID:         uuidOf(l.ID),
			Name:       l.Name,
			Platform:   l.Platform,
			Executable: l.Executable,
			Arguments:  l.Arguments,
			WorkingDir: l.WorkingDir,
		})
	}
	c.JSON(200, gin.H{
		"digest":      rel.Digest,
		"totalSize":   rel.TotalSize,
		"manifestUrl": "/api/v1/depot/manifests/" + rel.Digest,
		"launches":    launchDTOs,
	})
}

func (s *Server) handleDepotManifest(c *gin.Context) {
	digest := c.Param("digest")
	rel, err := s.db.GetReleaseByDigest(c.Request.Context(), digest)
	if err != nil {
		c.JSON(404, gin.H{"error": gin.H{"code": "not_found", "message": "manifest not found"}})
		return
	}
	c.Data(200, "application/json", rel.Manifest)
}

func (s *Server) handleDepotObject(c *gin.Context) {
	shaHex := c.Param("sha256")
	if len(shaHex) != 64 {
		c.Status(400)
		return
	}
	sha, err := hex.DecodeString(shaHex)
	if err != nil {
		c.Status(400)
		return
	}
	path := filepath.Join(s.depotRoot(), "objects", shaHex[:2], shaHex)
	f, err := os.Open(path)
	if err != nil {
		c.Status(404)
		return
	}
	defer f.Close()
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Type", "application/octet-stream")
	io.Copy(c.Writer, f)
	_ = sha
}
