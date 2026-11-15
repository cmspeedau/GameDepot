package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/saves"
)

// saveHistoryLimit is how many non-pinned versions a slot keeps beyond its head.
const saveHistoryLimit = 10

func (s *Server) objectPathBytes(sha []byte) string {
	h := hex.EncodeToString(sha)
	return s.cfg.DataDir + "/objects/" + h[:2] + "/" + h
}

// writeObjectFromStream content-addresses a stream into the general object store,
// deduplicating by SHA-256, and returns the object row id. The write is atomic: temp
// file, fsync, rename; an existing object with the same hash is reused, not rewritten.
func (s *Server) writeObjectFromStream(ctx context.Context, r io.Reader, contentType string) (pgtype.UUID, string, int64, error) {
	if err := os.MkdirAll(s.cfg.DataDir+"/objects", 0o755); err != nil {
		return pgtype.UUID{}, "", 0, err
	}
	tmp, err := os.CreateTemp(s.cfg.DataDir+"/objects", ".put-*")
	if err != nil {
		return pgtype.UUID{}, "", 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		tmp.Close()
		return pgtype.UUID{}, "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return pgtype.UUID{}, "", 0, err
	}
	tmp.Close()

	sum := h.Sum(nil)
	hexSum := hex.EncodeToString(sum)
	dst := s.objectPathBytes(sum)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		if err := os.MkdirAll(s.cfg.DataDir+"/objects/"+hexSum[:2], 0o755); err != nil {
			return pgtype.UUID{}, "", 0, err
		}
		if err := os.Rename(tmpPath, dst); err != nil {
			if _, statErr := os.Stat(dst); statErr != nil {
				return pgtype.UUID{}, "", 0, err
			}
		}
	}

	objID, err := s.db.GetOrCreateObjectByID(ctx, db.GetOrCreateObjectByIDParams{
		ID:          pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Sha256:      sum,
		Size:        n,
		ContentType: contentType,
	})
	if err != nil {
		return pgtype.UUID{}, "", 0, err
	}
	return objID, hexSum, n, nil
}

func scopeGranted(client db.Client, want string) bool {
	for _, s := range client.Scopes {
		if s == want {
			return true
		}
	}
	return false
}

// clientSaves resolves the caller and requires a scope, aborting the request otherwise.
func (s *Server) clientSaves(c *gin.Context, scope string) (db.Client, bool) {
	client := c.MustGet("client").(db.Client)
	if !scopeGranted(client, scope) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "forbidden", "message": "scope not granted"}})
		return client, false
	}
	return client, true
}

func (s *Server) slotRef(c *gin.Context) (gameID string, idx int32, ok bool) {
	gameID = c.Param("gameId")
	n, err := strconv.Atoi(c.Param("slot"))
	if err != nil || n < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "bad slot index"}})
		return "", 0, false
	}
	return gameID, int32(n), true
}

// uuidToPg / pgToUUIDStr helpers.
func pgFromStr(id string) pgtype.UUID {
	u, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func (s *Server) handleSavesList(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:read"); !ok {
		return
	}
	client := c.MustGet("client").(db.Client)
	var gameFilter pgtype.UUID
	if g := c.Query("game"); g != "" {
		gameFilter = pgFromStr(g)
	}
	rows, err := s.db.ListSlots(c.Request.Context(), db.ListSlotsParams{
		UserID: client.UserID,
		GameID: gameFilter,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal", "message": "list failed"}})
		return
	}
	out := make([]slotDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSlotDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

// handleSavePush stores an archive as a new version with optimistic concurrency.
func (s *Server) handleSavePush(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:write"); !ok {
		return
	}
	client := c.MustGet("client").(db.Client)
	gameIDStr, idx, ok := s.slotRef(c)
	if !ok {
		return
	}
	contentHash := c.GetHeader("X-Content-Hash")
	if contentHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request", "message": "X-Content-Hash required"}})
		return
	}
	baseSeq, _ := strconv.Atoi(c.GetHeader("X-Base-Seq"))
	fileCount, _ := strconv.Atoi(c.GetHeader("X-File-Count"))
	label := c.Query("label")

	// Write the archive object first; a lost CAS leaves an orphan object, which the
	// content-addressed store deduplicates, so no storage is wasted permanently.
	objID, hexSha, size, err := s.writeObjectFromStream(c.Request.Context(), c.Request.Body, "application/zstd")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal", "message": "store failed"}})
		return
	}

	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal", "message": "tx"}})
		return
	}
	defer tx.Rollback(c.Request.Context())
	qtx := s.db.WithTx(tx)

	slot, err := qtx.UpsertSlot(c.Request.Context(), db.UpsertSlotParams{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		UserID:    client.UserID,
		GameID:    pgFromStr(gameIDStr),
		SlotIndex: idx,
		Label:     label,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal", "message": "slot"}})
		return
	}

	headSeq := int32(0)
	headHash := ""
	currentHead := slot.HeadVersionID
	if slot.HeadVersionID.Valid {
		if hv, err := qtx.GetVersionByID(c.Request.Context(), slot.HeadVersionID); err == nil {
			headSeq = hv.VersionSeq
			headHash = hv.ContentHash
		}
	}

	switch saves.DecideSync(int32(baseSeq), contentHash, headSeq, headHash) {
	case saves.NoOp:
		if err := tx.Commit(c.Request.Context()); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"noop": true, "headVersionId": uuidOf(currentHead)})
		return
	case saves.Conflict:
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "conflict", "message": "head moved past base"},
			"headSeq": headSeq, "headVersionId": uuidOf(currentHead)})
		return
	case saves.Proceed:
	}

	seq, err := qtx.NextVersionSeq(c.Request.Context(), slot.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
		return
	}
	verID := uuid.New()
	ver, err := qtx.CreateSaveVersion(c.Request.Context(), db.CreateSaveVersionParams{
		ID:              pgtype.UUID{Bytes: verID, Valid: true},
		SlotID:          slot.ID,
		VersionSeq:      seq,
		Kind:            db.SaveVersionKindSave,
		ContentHash:     contentHash,
		ArchiveSha256:   hexSha,
		ArchiveObjectID: objID,
		Size:            size,
		FileCount:       int32(fileCount),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal", "message": "version"}})
		return
	}

	// Compare-and-set the head back to the value we read. If a concurrent push moved it,
	// zero rows update and we roll back with 409.
	updated, err := qtx.SetSlotHeadIfBase(c.Request.Context(), db.SetSlotHeadIfBaseParams{
		HeadVersionID:   pgtype.UUID{Bytes: verID, Valid: true},
		ID:              slot.ID,
		HeadVersionID_2: currentHead,
	})
	if err != nil || updated == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "conflict", "message": "concurrent push"}})
		return
	}

	// Retention: drop unpinned, non-head versions older than the window. Objects stay.
	if cands, err := qtx.PruneCandidates(c.Request.Context(), db.PruneCandidatesParams{SlotID: slot.ID, Offset: saveHistoryLimit}); err == nil {
		for _, cand := range cands {
			_ = qtx.DeleteVersionByID(c.Request.Context(), cand.ID)
		}
	}

	if err := tx.Commit(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
		return
	}
	c.JSON(http.StatusOK, toVersionDTO(ver))
}

// handleSaveDownload serves a slot's archive (head or a chosen version by ?seq=).
func (s *Server) handleSaveDownload(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:read"); !ok {
		return
	}
	ver, slot, ok := s.resolveVersion(c)
	if !ok {
		return
	}
	_ = slot
	if !ver.ArchiveObjectID.Valid {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "archive missing"}})
		return
	}
	obj, err := s.db.GetObjectByID(c.Request.Context(), ver.ArchiveObjectID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "object missing"}})
		return
	}
	f, err := os.Open(s.objectPathBytes(obj.Sha256))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "file missing"}})
		return
	}
	defer f.Close()
	c.Header("Content-Type", obj.ContentType)
	c.Header("ETag", `"`+hex.EncodeToString(obj.Sha256)+`"`)
	c.Status(http.StatusOK)
	io.Copy(c.Writer, f)
}

// resolveVersion loads a version by ?seq= within the caller's slot, or the head.
func (s *Server) resolveVersion(c *gin.Context) (db.SaveVersion, db.SaveSlot, bool) {
	client := c.MustGet("client").(db.Client)
	gameIDStr, idx, ok := s.slotRef(c)
	if !ok {
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	slot, err := s.db.GetSlot(c.Request.Context(), db.GetSlotParams{
		UserID: client.UserID, GameID: pgFromStr(gameIDStr), SlotIndex: idx,
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "slot"}})
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	if q := c.Query("seq"); q != "" {
		vers, err := s.db.ListVersions(c.Request.Context(), slot.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
			return db.SaveVersion{}, db.SaveSlot{}, false
		}
		want, _ := strconv.Atoi(q)
		for _, v := range vers {
			if int(v.VersionSeq) == want {
				return v, slot, true
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "version"}})
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	if !slot.HeadVersionID.Valid {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "no head"}})
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	ver, err := s.db.GetVersionByID(c.Request.Context(), slot.HeadVersionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "version"}})
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	return ver, slot, true
}

func (s *Server) handleSaveVersions(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:read"); !ok {
		return
	}
	client := c.MustGet("client").(db.Client)
	gameIDStr, idx, ok := s.slotRef(c)
	if !ok {
		return
	}
	slot, err := s.db.GetSlot(c.Request.Context(), db.GetSlotParams{UserID: client.UserID, GameID: pgFromStr(gameIDStr), SlotIndex: idx})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found"}})
		return
	}
	vers, _ := s.db.ListVersions(c.Request.Context(), slot.ID)
	out := make([]versionDTO, 0, len(vers))
	for _, v := range vers {
		out = append(out, toVersionDTO(v))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) versionOwnedBy(c *gin.Context) (db.SaveVersion, db.SaveSlot, bool) {
	client := c.MustGet("client").(db.Client)
	vid, err := uuid.Parse(c.Param("versionId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "bad_request"}})
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	ver, err := s.db.GetVersionByID(c.Request.Context(), pgtype.UUID{Bytes: vid, Valid: true})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found"}})
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	slot, err := s.db.GetSlotByID(c.Request.Context(), ver.SlotID)
	if err != nil || !pgUUIDEq(slot.UserID, client.UserID) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found"}})
		return db.SaveVersion{}, db.SaveSlot{}, false
	}
	return ver, slot, true
}

func (s *Server) handleSavePin(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:write"); !ok {
		return
	}
	ver, _, ok := s.versionOwnedBy(c)
	if !ok {
		return
	}
	pinned := c.Query("pinned") != "false"
	_ = s.db.SetVersionPinned(c.Request.Context(), db.SetVersionPinnedParams{ID: ver.ID, Pinned: pinned})
	c.JSON(http.StatusOK, gin.H{"pinned": pinned})
}

func (s *Server) handleSaveDeleteVersion(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:write"); !ok {
		return
	}
	ver, slot, ok := s.versionOwnedBy(c)
	if !ok {
		return
	}
	if pgUUIDEq(ver.ID, slot.HeadVersionID) {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "conflict", "message": "cannot delete head"}})
		return
	}
	if ver.Pinned {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "conflict", "message": "version pinned"}})
		return
	}
	_ = s.db.DeleteVersionByID(c.Request.Context(), ver.ID)
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// handleSaveRestore makes an old version the head by adding a new `restore` version
// that reuses the same archive object.
func (s *Server) handleSaveRestore(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:write"); !ok {
		return
	}
	ver, slot, ok := s.versionOwnedBy(c)
	if !ok {
		return
	}
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
		return
	}
	defer tx.Rollback(c.Request.Context())
	qtx := s.db.WithTx(tx)

	seq, err := qtx.NextVersionSeq(c.Request.Context(), slot.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
		return
	}
	newID := uuid.New()
	newVer, err := qtx.CreateSaveVersion(c.Request.Context(), db.CreateSaveVersionParams{
		ID:              pgtype.UUID{Bytes: newID, Valid: true},
		SlotID:          slot.ID,
		VersionSeq:      seq,
		Kind:            db.SaveVersionKindRestore,
		ContentHash:     ver.ContentHash,
		ArchiveSha256:   ver.ArchiveSha256,
		ArchiveObjectID: ver.ArchiveObjectID,
		Size:            ver.Size,
		FileCount:       ver.FileCount,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
		return
	}
	updated, err := qtx.SetSlotHeadIfBase(c.Request.Context(), db.SetSlotHeadIfBaseParams{
		HeadVersionID:   pgtype.UUID{Bytes: newID, Valid: true},
		ID:              slot.ID,
		HeadVersionID_2: slot.HeadVersionID,
	})
	if err != nil || updated == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "conflict", "message": "head moved"}})
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
		return
	}
	c.JSON(http.StatusOK, toVersionDTO(newVer))
}

// handleSaveManifest returns the user-defined save roots for a game.
func (s *Server) handleSaveManifest(c *gin.Context) {
	if _, ok := s.clientSaves(c, "saves:read"); !ok {
		return
	}
	paths, err := s.db.ListSavePaths(c.Request.Context(), pgFromStr(c.Param("gameId")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal"}})
		return
	}
	out := make([]savePathDTO, 0, len(paths))
	for _, p := range paths {
		out = append(out, savePathDTO{
			ID: uuidOf(p.ID), Platform: p.Platform, Kind: p.Kind,
			Template: p.Template, Recursive: p.Recursive,
		})
	}
	c.JSON(http.StatusOK, gin.H{"gameId": c.Param("gameId"), "paths": out})
}

func pgUUIDEq(a, b pgtype.UUID) bool {
	if !a.Valid || !b.Valid {
		return false
	}
	return a.Bytes == b.Bytes
}

func uuidOf(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func timeOf(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format("2006-01-02T15:04:05Z")
}

type slotDTO struct {
	ID            string `json:"id"`
	GameID        string `json:"gameId"`
	SlotIndex     int32  `json:"slotIndex"`
	Label         string `json:"label"`
	HeadVersionID string `json:"headVersionId"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

type versionDTO struct {
	ID            string `json:"id"`
	Seq           int32  `json:"seq"`
	Kind          string `json:"kind"`
	ContentHash   string `json:"contentHash"`
	ArchiveSHA256 string `json:"archiveSha256"`
	Size          int64  `json:"size"`
	FileCount     int32  `json:"fileCount"`
	Pinned        bool   `json:"pinned"`
	CreatedAt     string `json:"createdAt"`
}

type savePathDTO struct {
	ID        string `json:"id"`
	Platform  string `json:"platform"`
	Kind      string `json:"kind"`
	Template  string `json:"template"`
	Recursive bool   `json:"recursive"`
}

func toSlotDTO(s db.SaveSlot) slotDTO {
	return slotDTO{
		ID: uuidOf(s.ID), GameID: uuidOf(s.GameID), SlotIndex: s.SlotIndex,
		Label: s.Label, HeadVersionID: uuidOf(s.HeadVersionID),
		CreatedAt: timeOf(s.CreatedAt), UpdatedAt: timeOf(s.UpdatedAt),
	}
}

func toVersionDTO(v db.SaveVersion) versionDTO {
	return versionDTO{
		ID: uuidOf(v.ID), Seq: v.VersionSeq, Kind: string(v.Kind),
		ContentHash: v.ContentHash, ArchiveSHA256: v.ArchiveSha256,
		Size: v.Size, FileCount: v.FileCount, Pinned: v.Pinned,
		CreatedAt: timeOf(v.CreatedAt),
	}
}
