--| tier: standard
--| transaction: required
-- Marks the directory with id and every directory beneath it deleting,
-- advancing each changed row's version. The walk descends through rows
-- already deleting, so a repeated mark reaches a straggler, and UNION ends
-- it on a cycle. The version guards the anchor only: a directory already
-- deleting is the mark's retry at any version. A transaction is required:
-- mark_directory_files_deleting follows in it, under the tree lock.
UPDATE blobfs_directory
SET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE status <> 'deleting' AND id IN (
  WITH RECURSIVE branch (id) AS (
      SELECT d.id
      FROM blobfs_directory d
      WHERE d.id = {{id:uuid}} AND d.parent_id IS NOT NULL
        AND ({{version:bigint}} IS NULL OR d.version = {{version:bigint}} OR d.status = 'deleting')
    UNION
      SELECT d.id
      FROM blobfs_directory d
      JOIN branch b ON d.parent_id = b.id
  )
  SELECT b.id
  FROM branch b
)
