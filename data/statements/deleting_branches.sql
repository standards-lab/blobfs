--| tier: standard
-- The roots of the branches being deleted, for Directories.Deleting: each
-- directory that is deleting under a parent that is active, which is the
-- directory a mark named, since a mark reaches every directory beneath it.
-- A directory deleting under a deleting parent is inside a branch whose
-- root is listed already, and the root, which a mark never reaches, is
-- never listed. The rows come in id order, one page of at most fetch rows
-- past offset through the query library's paging pattern, which an engine
-- without the standard form overrides; the caller binds offset 0, so a
-- sweeper reads the first roots and, having removed them, the next.
--
-- The status predicate is an engine's to index: the postgres migrations
-- keep a partial index of the deleting directories, so the statement reads
-- the deleting rows alone and each candidate's parent through the primary
-- key. An engine without one reads the directory table.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
JOIN blobfs_directory p ON p.id = d.parent_id
WHERE d.status = 'deleting' AND p.status = 'active'
ORDER BY d.id
{{> sql.paging}}
