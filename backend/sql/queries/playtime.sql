-- name: AddPlaytime :one
INSERT INTO playtime (user_id, game_id, seconds)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, game_id) DO UPDATE
SET seconds = playtime.seconds + EXCLUDED.seconds,
    last_played_at = now()
RETURNING user_id, game_id, seconds, last_played_at;

-- name: ListPlaytimeForUser :many
SELECT game_id, seconds, last_played_at
FROM playtime WHERE user_id = $1 ORDER BY last_played_at DESC;
