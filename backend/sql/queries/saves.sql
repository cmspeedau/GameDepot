-- name: GetOrCreateObjectByID :one
INSERT INTO objects (id, sha256, size, content_type)
VALUES ($1, $2, $3, $4)
ON CONFLICT (sha256) DO UPDATE SET size = objects.size
RETURNING id;

-- name: DeleteObjectByID :exec
DELETE FROM objects WHERE id = $1;

-- name: GetSlot :one
SELECT id, user_id, game_id, slot_index, label, head_version_id, created_at, updated_at
FROM save_slots WHERE user_id = $1 AND game_id = $2 AND slot_index = $3;

-- name: GetSlotByID :one
SELECT id, user_id, game_id, slot_index, label, head_version_id, created_at, updated_at
FROM save_slots WHERE id = $1;

-- name: ListSlots :many
SELECT id, user_id, game_id, slot_index, label, head_version_id, created_at, updated_at
FROM save_slots
WHERE user_id = $1 AND (sqlc.arg(game_id)::uuid IS NULL OR game_id = sqlc.arg(game_id))
ORDER BY game_id ASC, slot_index ASC;

-- name: CreateSlot :one
INSERT INTO save_slots (id, user_id, game_id, slot_index, label)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, game_id, slot_index, label, head_version_id, created_at, updated_at;

-- name: UpsertSlot :one
-- Get-or-create a slot and lock the row for the enclosing transaction, so the head
-- compare-and-set below is race-safe. On conflict the existing row is returned.
INSERT INTO save_slots (id, user_id, game_id, slot_index, label)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, game_id, slot_index) DO UPDATE SET label = save_slots.label
RETURNING id, user_id, game_id, slot_index, label, head_version_id, created_at, updated_at;

-- name: SetSlotHeadIfBase :execrows
-- Optimistic concurrency: move the head only if it currently equals the base the
-- client built on (base may be NULL for a first push).
UPDATE save_slots
SET head_version_id = $1, updated_at = now()
WHERE id = $2
  AND head_version_id IS NOT DISTINCT FROM $3;

-- name: NextVersionSeq :one
SELECT COALESCE(max(version_seq), 0) + 1 FROM save_versions WHERE slot_id = $1;

-- name: CreateSaveVersion :one
INSERT INTO save_versions (id, slot_id, version_seq, kind, content_hash, archive_sha256, archive_object_id, size, file_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, slot_id, version_seq, kind, content_hash, archive_sha256, archive_object_id, size, file_count, pinned, created_at;

-- name: GetVersionByID :one
SELECT id, slot_id, version_seq, kind, content_hash, archive_sha256, archive_object_id, size, file_count, pinned, created_at
FROM save_versions WHERE id = $1;

-- name: ListVersions :many
SELECT id, slot_id, version_seq, kind, content_hash, archive_sha256, archive_object_id, size, file_count, pinned, created_at
FROM save_versions WHERE slot_id = $1 ORDER BY version_seq DESC;

-- name: SetVersionPinned :exec
UPDATE save_versions SET pinned = $2 WHERE id = $1;

-- name: DeleteVersionByID :exec
DELETE FROM save_versions WHERE id = $1 AND pinned = false;

-- name: PruneCandidates :many
-- Versions past the retained count, oldest first, excluding the head and pinned rows.
SELECT v.id, v.archive_object_id FROM save_versions v
JOIN save_slots s ON s.id = v.slot_id
WHERE v.slot_id = $1 AND v.pinned = false AND v.id IS DISTINCT FROM s.head_version_id
ORDER BY v.version_seq ASC
OFFSET $2;
