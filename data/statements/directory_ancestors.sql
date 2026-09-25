--| tier: standard
-- The chain from the directory with id up to the root, each row's id,
-- parent_id, and name, in no set order; its cost is the directory's depth.
-- UNION, not UNION ALL, discards a directory the walk has visited, so the
-- walk terminates on a cycle in the tree.
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
