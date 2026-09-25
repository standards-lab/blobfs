--| tier: standard
--| transaction: required
-- Marks every file in the directory with id and the directories beneath
-- it deleting, advancing each changed row's version, over
-- mark_directory_deleting's walk. The update takes each row's lock, so it
-- waits on a hold.
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
