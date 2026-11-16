package server

import (
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

// launchDTO is one launch option of a version, as served on the release endpoint.
// Executable is a path relative to the install root; the client validates it stays
// inside the root before spawning.
type launchDTO struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Platform   string   `json:"platform"`
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
	WorkingDir string   `json:"workingDir"`
}

func (s *Server) handleCreateLaunch(c *gin.Context) {
	versionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, "bad version id")
		return
	}
	gv, err := s.db.GetGameVersion(c.Request.Context(), pgtype.UUID{Bytes: versionID, Valid: true})
	if err != nil {
		c.String(http.StatusNotFound, "version not found")
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	platform := strings.TrimSpace(c.PostForm("platform"))
	executable := strings.TrimSpace(c.PostForm("executable"))
	if executable == "" || platform == "" {
		s.redirectFlash(c, "/dashboard/games/"+uuidOf(gv.GameID), "missing+fields")
		return
	}
	if name == "" {
		name = path.Base(executable)
	}
	// Space-separated argument list, matching the dashboard form. Arguments arrive at
	// execve as an array; they never pass through a shell.
	arguments := strings.Fields(c.PostForm("arguments"))
	_, err = s.db.CreateLaunch(c.Request.Context(), db.CreateLaunchParams{
		ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
		VersionID:  pgtype.UUID{Bytes: versionID, Valid: true},
		Platform:   platform,
		Name:       name,
		Executable: executable,
		Arguments:  arguments,
		WorkingDir: strings.TrimSpace(c.PostForm("working_dir")),
	})
	if err != nil {
		s.redirectFlash(c, "/dashboard/games/"+uuidOf(gv.GameID), "launch+failed")
		return
	}
	s.redirectFlash(c, "/dashboard/games/"+uuidOf(gv.GameID), "launch+added")
}

func (s *Server) handleDeleteLaunch(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, "bad id")
		return
	}
	launch, err := s.db.GetLaunch(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if err := s.db.DeleteLaunch(c.Request.Context(), pgtype.UUID{Bytes: id, Valid: true}); err != nil {
		c.String(http.StatusInternalServerError, "delete failed")
		return
	}
	gv, err := s.db.GetGameVersion(c.Request.Context(), launch.VersionID)
	gamePath := "/dashboard/games"
	if err == nil {
		gamePath = "/dashboard/games/" + uuidOf(gv.GameID)
	}
	s.redirectFlash(c, gamePath, "launch+removed")
}

func (s *Server) redirectFlash(c *gin.Context, to, flash string) {
	c.Redirect(http.StatusFound, to+"?flash="+flash)
}
