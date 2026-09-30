--| tier: native
--| native: a text[] parameter indexed by the recursion depth inside WITH RECURSIVE, so a path of
--| [native]: any depth resolves in one round trip where the baseline reads one child per segment.
--| [native]: Port: an engine must bind an ordered list as one parameter and index it by position
--| [native]: inside a recursive query (SQL Server's OPENJSON WITH ORDINALITY over a JSON array,
--| [native]: Oracle's JSON_TABLE, SQLite's json_each with its key), and its driver must encode a Go
--| [native]: []string as that parameter; an engine without one keeps the baseline's walk.
-- The deepest directory reached below start_id along segments, normalized
-- names in path order, with its depth; no row means the start does not
-- exist. A step past the last segment compares a name with NULL, so a
-- cycle in the tree cannot extend the walk. The row is selected by the
-- maximum depth rather than sorted and cut, which saves a sort's buffers.
WITH RECURSIVE walk (id, parent_id, name, status, version, created_at, updated_at, depth) AS (
    SELECT {{> blobfs.directory_columns}}, CAST(0 AS integer)
    FROM blobfs_directory d
    WHERE d.id = {{start_id:uuid}}
  UNION ALL
    SELECT {{> blobfs.directory_columns}}, w.depth + 1
    FROM walk w
    JOIN blobfs_directory d ON d.parent_id = w.id AND d.name = (CAST({{segments}} AS text[]))[w.depth + 1]
)
SELECT w.id, w.parent_id, w.name, w.status, w.version, w.created_at, w.updated_at, w.depth
FROM walk w
WHERE w.depth = (SELECT max(x.depth) FROM walk x)
