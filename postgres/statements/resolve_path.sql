--| tier: native
--| native: a text[] parameter indexed by the recursion depth inside WITH RECURSIVE, so a path of
--| [native]: any depth resolves in one round trip where the baseline reads one child per segment.
--| [native]: Port: an engine must bind an ordered list as one parameter and index it by position
--| [native]: inside a recursive query (SQL Server's OPENJSON WITH ORDINALITY over a JSON array,
--| [native]: Oracle's JSON_TABLE, SQLite's json_each with its key), and its driver must encode a Go
--| [native]: []string as that parameter; an engine without one keeps the baseline's walk.
-- Path resolution in one statement: the deepest directory reached below
-- start_id along segments, a text[] of normalized directory names in
-- path order, with its depth. The anchor is the start itself at depth 0,
-- and each step joins the child of the last row whose name is the
-- segment at the next depth, so the walk stops by itself at the first
-- segment that names no directory. The final SELECT keeps the row at the
-- greatest depth: a depth equal to the number of segments is the resolved
-- directory, a smaller depth names the last directory reached and so the
-- segment that failed, and no row at all means the start does not exist.
-- The variant returns the row and its depth; the store spells the failing
-- prefix from the segments it holds. No segments returns the start at
-- depth 0, since an empty array has no element at position 1. The
-- segments bind as one parameter, so a name is never spliced into the
-- text, whatever characters it carries. The row is selected by the
-- maximum depth rather than sorted and cut, which the spike's measurement
-- showed saves three buffers on a sort the walk does not need.
WITH RECURSIVE walk (id, parent_id, name, version, created_at, updated_at, depth) AS (
    SELECT {{> blobfs.directory_columns}}, CAST(0 AS integer)
    FROM blobfs_directory d
    WHERE d.id = {{start_id:uuid}}
  UNION ALL
    SELECT {{> blobfs.directory_columns}}, w.depth + 1
    FROM walk w
    JOIN blobfs_directory d ON d.parent_id = w.id AND d.name = (CAST({{segments}} AS text[]))[w.depth + 1]
)
SELECT w.id, w.parent_id, w.name, w.version, w.created_at, w.updated_at, w.depth
FROM walk w
WHERE w.depth = (SELECT max(x.depth) FROM walk x)
