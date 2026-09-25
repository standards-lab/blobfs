--| tier: standard
--| transaction: required
-- Marks the directory with id and every directory beneath it deleting,
-- advancing each changed row's version. The walk descends through
-- directories already deleting, so a repeated mark reaches a straggler, and
-- the anchor's parent_id predicate keeps the root out. UNION discards a
-- directory the walk has visited, so the walk terminates on a cycle. A
-- transaction is required: it runs under the tree lock beside
-- mark_directory_files_deleting, so both walk one branch.
UPDATE blobfs_directory
SET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE status <> 'deleting' AND id IN (
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
