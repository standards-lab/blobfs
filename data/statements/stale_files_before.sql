--| tier: standard
-- The stale files last written before the instant before, for
-- Store.Sweep's reclaim of the protocols a caller stopped partway: each
-- pending row a Files.Create or Files.Ensure inserted whose write never
-- completed, and each deleting row whose Files.Delete committed and whose
-- Files.Purge never ran, older than the age the caller chose. Either row
-- holds its name, and a deleting one is hidden from the listings, so
-- nothing but a sweep finds it. The caller computes before from its own
-- clock and the age; updated_at is stamped by the database, so the two
-- clocks' skew is part of the margin. The rows come oldest first, ties
-- broken by id, one page of at most fetch rows past offset through the
-- query library's paging pattern, which an engine without the standard
-- form overrides; the caller binds offset 0, since each row it reclaims
-- leaves the set.
--
-- The status predicate and the order are an engine's to index: the
-- postgres migrations keep a partial index of the pending and deleting
-- files by (updated_at, id) under the same predicate, so the statement
-- reads the oldest stale rows in order and stops at the page. An engine
-- without one reads the file table.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.status IN ('pending', 'deleting') AND f.updated_at < {{before:timestamp with time zone}}
ORDER BY f.updated_at, f.id
{{> sql.paging}}
