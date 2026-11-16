package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

// maxPlaytimeDelta bounds one reported session: 31,536,000 seconds is a year. A larger
// value is a bug or a lie, not playtime (see docs/CLIENT-API.md, Playtime).
const maxPlaytimeDelta = 31_536_000

type playtimeDTO struct {
	GameID       string `json:"gameId"`
	Seconds      int64  `json:"seconds"`
	LastPlayedAt string `json:"lastPlayedAt"`
}

func (s *Server) handlePlaytimeList(c *gin.Context) {
	client := c.MustGet("client").(db.Client)
	rows, err := s.db.ListPlaytimeForUser(c.Request.Context(), client.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal", "message": "list failed"}})
		return
	}
	out := make([]playtimeDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, playtimeDTO{GameID: uuidOf(r.GameID), Seconds: r.Seconds, LastPlayedAt: timeOf(r.LastPlayedAt)})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) handlePlaytimeAdd(c *gin.Context) {
	client := c.MustGet("client").(db.Client)
	var body struct {
		GameID  string `json:"gameId"`
		Seconds int64  `json:"seconds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "invalid body"}})
		return
	}
	gameID, err := uuid.Parse(body.GameID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "bad game id"}})
		return
	}
	if body.Seconds < 1 || body.Seconds > maxPlaytimeDelta {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "seconds must be between 1 and 31536000"}})
		return
	}
	gid := pgtype.UUID{Bytes: gameID, Valid: true}
	if _, err := s.db.GetGameByID(c.Request.Context(), gid); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "game not found"}})
		return
	}
	row, err := s.db.AddPlaytime(c.Request.Context(), db.AddPlaytimeParams{
		UserID:  client.UserID,
		GameID:  gid,
		Seconds: body.Seconds,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal", "message": "update failed"}})
		return
	}
	c.JSON(http.StatusOK, playtimeDTO{GameID: uuidOf(row.GameID), Seconds: row.Seconds, LastPlayedAt: timeOf(row.LastPlayedAt)})
}
