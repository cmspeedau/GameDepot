package server

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

// handleGameRecipe serves the runner recipe (if any) for a game to a paired client.
func (s *Server) handleGameRecipe(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "bad game id"}})
		return
	}
	gameID := pgtype.UUID{Bytes: id, Valid: true}

	var recipe db.RunnerRecipe
	if v := c.Query("versionId"); v != "" {
		vid, err := uuid.Parse(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "bad version id"}})
			return
		}
		recipe, err = s.db.GetRunnerRecipeForVersion(c.Request.Context(), db.GetRunnerRecipeForVersionParams{
			GameID:    gameID,
			VersionID: pgtype.UUID{Bytes: vid, Valid: true},
		})
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "recipe not found"}})
			return
		}
	} else {
		recipe, err = s.db.GetRunnerRecipeForGame(c.Request.Context(), gameID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "recipe not found"}})
			return
		}
	}

	var profile any
	if len(recipe.Profile) > 0 {
		_ = json.Unmarshal(recipe.Profile, &profile)
	}
	c.JSON(http.StatusOK, gin.H{
		"id":           uuid.UUID(recipe.ID.Bytes).String(),
		"gameId":       uuid.UUID(recipe.GameID.Bytes).String(),
		"versionId":    func() string {
			if recipe.VersionID.Valid {
				return uuid.UUID(recipe.VersionID.Bytes).String()
			}
			return ""
		}(),
		"profile":      profile,
		"source":       recipe.Source,
		"compatibility": recipe.Compatibility,
		"createdAt":    recipe.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"updatedAt":    recipe.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}