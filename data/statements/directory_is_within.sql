--| tier: standard
-- The cycle check: a count above zero when ancestor_id is on the chain from
-- id up to the root, id included; its cost is the depth of id. UNION, not
-- UNION ALL, discards a directory the walk has visited, so the walk
-- terminates on a cycle in the tree.
WITH RECURSIVE up (id, parent_id) AS (
    SELECT d.id, d.parent_id
    FROM blobfs_directory d
    WHERE d.id = {{id:uuid}}
  UNION
    SELECT d.id, d.parent_id
    FROM blobfs_directory d
    JOIN up ON up.parent_id = d.id
)
SELECT COUNT(*) AS matches
FROM up
WHERE up.id = {{ancestor_id:uuid}}
