--| tier: standard
-- The pending files last written before the instant before, for
-- Store.Sweep's reclaim of abandoned writes: each row a Files.Create or
-- Files.Ensure inserted whose write never completed, older than the age
-- the caller chose. The caller computes before from its own clock and the
-- age; updated_at is stamped by the database, so the two clocks' skew is
-- part of the margin. The rows come oldest first, ties broken by id, one
-- page of at most fetch rows past offset through the query library's
-- paging pattern, which an engine without the standard form overrides; the
-- caller binds offset 0, since each row it reclaims leaves the set.
--
-- The status predicate and the order are an engine's to index: the
-- postgres migrations keep a partial index of the pending files by
-- (updated_at, id), so the statement reads the oldest pending rows in
-- order and stops at the page. An engine without one reads the file table.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.status = 'pending' AND f.updated_at < {{before:timestamp with time zone}}
ORDER BY f.updated_at, f.id
{{> sql.paging}}
