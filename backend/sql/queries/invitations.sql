-- name: CreateInvitation :one
INSERT INTO invitations (id, code_hash, created_by, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id, code_hash, created_by, expires_at, created_at;

-- name: GetInvitationByCodeHash :one
SELECT id, code_hash, created_by, used_by, expires_at, created_at, used_at
FROM invitations
WHERE code_hash = $1;

-- name: MarkInvitationUsed :exec
UPDATE invitations SET used_by = $2, used_at = now() WHERE id = $1 AND used_at IS NULL;

-- name: ListInvitations :many
SELECT id, code_hash, created_by, used_by, expires_at, created_at, used_at
FROM invitations
ORDER BY created_at DESC;
