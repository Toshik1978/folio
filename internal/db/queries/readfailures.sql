-- name: UpsertReadFailure :exec
INSERT INTO book_read_failures (book_id, flow, reason, detail, fingerprint, reader_version, recorded_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (book_id, flow) DO UPDATE SET reason         = excluded.reason,
                                          detail         = excluded.detail,
                                          fingerprint    = excluded.fingerprint,
                                          reader_version = excluded.reader_version,
                                          recorded_at    = excluded.recorded_at;

-- name: GetReadFailure :one
SELECT *
FROM book_read_failures
WHERE book_id = ?
  AND flow = ?;

-- name: DeleteReadFailure :exec
DELETE
FROM book_read_failures
WHERE book_id = ?
  AND flow = ?;

-- name: DeleteReadFailuresForBook :exec
DELETE
FROM book_read_failures
WHERE book_id = ?;

-- ListMetadataFailureFiles returns every file of each book of a library that
-- has a metadata-flow broken-read record of the given reader version, with that
-- record's fingerprint, for the post-sync re-check. Rows are ordered by
-- source_path so the entries of one archive are contiguous.
-- name: ListMetadataFailureFiles :many
SELECT f.book_id      AS book_id,
       f.id           AS file_id,
       f.file_format  AS file_format,
       f.source_path  AS source_path,
       rf.fingerprint AS fingerprint
FROM book_read_failures rf
         JOIN books b ON b.id = rf.book_id
         JOIN book_files f ON f.book_id = rf.book_id
WHERE b.library_id = sqlc.arg('library_id')
  AND rf.flow = 'metadata'
  AND rf.reader_version = sqlc.arg('reader_version')
ORDER BY f.source_path, f.book_id;
