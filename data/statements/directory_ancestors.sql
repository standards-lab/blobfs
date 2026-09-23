--| tier: standard
-- The chain from a directory up to the root, for Directories.Path: one
-- recursive walk that starts at the directory with id and follows
-- parent_id upward, so its cost is the directory's depth and never the
-- size of the tree. The rows come back root first (the greatest depth),
-- each with its parent_id and name, and the root's name is /. A
-- directory that does not exist yields no rows. The anchor's depth is cast
-- so the recursive column has a type on every engine.
WITH RECURSIVE ancestors (id, parent_id, name, depth) AS (
    SELECT d.id, d.parent_id, d.name, CAST(0 AS integer)
    FROM blobfs_directory d
    WHERE d.id = {{id:uuid}}
  UNION ALL
    SELECT d.id, d.parent_id, d.name, a.depth + 1
    FROM blobfs_directory d
    JOIN ancestors a ON a.parent_id = d.id
)
SELECT a.parent_id, a.name
FROM ancestors a
ORDER BY a.depth DESC
