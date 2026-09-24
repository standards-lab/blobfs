--| tier: standard
-- The cycle check of a directory move: whether the directory with id lies
-- within the subtree of the directory with ancestor_id, that directory
-- itself included. One recursive walk starts at id and follows parent_id
-- upward, so its cost is the depth of id and never the size of the tree,
-- and the count says whether ancestor_id was met on the way. A move of a
-- directory D under a new parent P runs it with id = P and ancestor_id =
-- D: a count above zero means P is D or one of D's descendants, and the
-- move would make D its own ancestor. A directory that does not exist
-- yields no rows and a count of zero; the update that follows then fails
-- the foreign key.
--
-- The walk combines its steps with UNION, not UNION ALL, so a row the walk
-- has already produced is discarded rather than joined again. That is its
-- termination guarantee on a cycle, which two opposing concurrent moves on
-- a variant without a tree lock can leave: the walk stops once it returns
-- to a directory it has visited, after at most one step per directory on
-- the chain, and the count is then whether ancestor_id is on the chain from
-- id, the loop included. On a tree without a cycle no row repeats, so UNION
-- discards nothing and the count is zero or one.
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
