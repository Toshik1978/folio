-- name: GetLibrary :one
SELECT *
FROM libraries
WHERE id = ?;

-- name: ListLibraries :many
SELECT *
FROM libraries
ORDER BY created_at;

-- name: ListLibrariesWithBookCount :many
SELECT s.*, COUNT(b.id) AS book_count
FROM libraries s
         LEFT JOIN books b ON b.library_id = s.id
GROUP BY s.id
ORDER BY s.created_at;

-- name: InsertLibrary :one
INSERT INTO libraries (name, type, path, sync_interval_seconds, status, created_at)
VALUES (?, ?, ?, ?, 'active', ?)
RETURNING id;

-- name: UpdateLibrary :exec
UPDATE libraries
SET name                  = ?,
    path                  = ?,
    sync_interval_seconds = ?
WHERE id = ?;

-- name: UpdateLibraryStatus :exec
UPDATE libraries
SET status          = ?,
    purge_at        = ?,
    last_sync_error = ?
WHERE id = ?;

-- UpdateLibraryLastSync stamps a successful (or skipped-unchanged) sync. It
-- also resets status to 'active' so a library that previously failed recovers
-- as soon as a later sync succeeds, instead of wearing the error badge forever.
-- A library deleted mid-sync (status flipped to 'pending_purge' while the run
-- was in flight) is excluded: resurrecting it to 'active' would silently cancel
-- the purge while leaving purge_at set.
-- name: UpdateLibraryLastSync :exec
UPDATE libraries
SET status          = 'active',
    last_sync_at    = ?,
    last_sync_error = NULL
WHERE id = ?
  AND status != 'pending_purge';

-- name: UpdateLibrarySyncError :exec
UPDATE libraries
SET status          = 'error',
    last_sync_error = ?
WHERE id = ?;

-- name: UpdateLibraryCheckpoint :exec
UPDATE libraries
SET checkpoint = ?
WHERE id = ?;

-- name: DeleteLibrary :exec
DELETE
FROM libraries
WHERE id = ?;

-- name: ListPendingPurgeLibraries :many
SELECT *
FROM libraries
WHERE status = 'pending_purge'
  AND purge_at IS NOT NULL
  AND purge_at <= ?;

