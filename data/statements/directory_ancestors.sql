--| tier: standard
-- The chain from a directory up to the root, for Directories.Path: one
-- recursive walk that starts at the directory with id and follows
-- parent_id upward, so its cost is the directory's depth and never the
-- size of the tree. Each row carries a directory's id, its parent_id, and
-- its name, and the root's name is /. The rows come in no set order: the
-- caller assembles the chain by following parent_id from id, which needs
-- no depth column. A directory that does not exist yields no rows.
--
-- The walk combines its steps with UNION, not UNION ALL, so a row the walk
-- has already produced is discarded rather than joined again. That is its
-- termination guarantee on a cycle, which two opposing concurrent moves on
-- a variant without a tree lock can leave: the walk stops once it returns
-- to a directory it has visited, after at most one step per directory on
-- the chain, and the caller finds the chain loops instead of reaching the
-- root. On a tree without a cycle no row repeats, so UNION discards nothing.
WITH RECURSIVE ancestors (id, parent_id, name) AS (
    SELECT d.id, d.parent_id, d.name
    FROM blobfs_directory d
    WHERE d.id = {{id:uuid}}
  UNION
    SELECT d.id, d.parent_id, d.name
    FROM blobfs_directory d
    JOIN ancestors a ON a.parent_id = d.id
)
SELECT a.id, a.parent_id, a.name
FROM ancestors a
