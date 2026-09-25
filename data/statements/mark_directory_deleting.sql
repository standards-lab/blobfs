--| tier: standard
--| transaction: required
-- The first step of a branch's delete: marks the directory with id and
-- every directory beneath it deleting, advances each one's version, and
-- stamps updated_at. One recursive walk starts at the directory and follows
-- parent_id downward, so its cost is the size of the branch and never the
-- size of the tree. The walk descends through directories already deleting,
-- so a mark repeated after a straggler's create reaches the straggler. The
-- status predicate leaves a row already deleting as it is, so a repeated
-- mark advances no version twice. The anchor's parent_id predicate keeps the
-- statement from ever marking the root, which Go refuses before this runs.
-- The affected count is the number of directories this mark moved to
-- deleting; none means the directory does not exist or its branch is
-- marked already, which the caller reads the row to tell apart. It requires
-- a transaction because it runs under the tree lock beside
-- mark_directory_files_deleting, so the branch the two walk is one branch.
--
-- The walk combines its steps with UNION, not UNION ALL, so a row the walk
-- has already produced is discarded rather than joined again: the walk
-- terminates on a cycle, which two opposing moves on a variant without a
-- tree lock can leave.
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
