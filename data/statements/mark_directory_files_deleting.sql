--| tier: standard
--| transaction: required
-- The second step of a branch's delete: marks every file in the directory
-- with id and in every directory beneath it deleting, advances each one's
-- version, and stamps updated_at, as the first step of a file's two-phase
-- delete does for one row. The walk is mark_directory_deleting's, run after
-- it in the same transaction under the tree lock, and the status predicate
-- leaves a row already deleting as it is, so a repeated mark converges.
-- The affected count is the number of files this mark moved to deleting.
-- The update takes each file's row lock, so it waits on a Files.Hold
-- another transaction took, as delete_file does. The anchor's parent_id
-- predicate keeps the root's files out, as there.
UPDATE blobfs_file
SET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE status <> 'deleting' AND directory_id IN (
  WITH RECURSIVE branch (id) AS (
      SELECT d.id
      FROM blobfs_directory d
      WHERE d.id = {{id:uuid}} AND d.parent_id IS NOT NULL
    UNION
      SELECT d.id
      FROM blobfs_directory d
      JOIN branch b ON d.parent_id = b.id
  )
  SELECT b.id
  FROM branch b
)
