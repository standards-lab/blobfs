--| tier: standard
-- The pending and deleting files last written before the instant before,
-- oldest first with ties broken by id, one page past offset. The predicate
-- and order are the ones the postgres migrations' partial index
-- blobfs_ix_file_stale covers, so an indexed read stops at the page.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.status IN ('pending', 'deleting') AND f.updated_at < {{before:timestamp with time zone}}
ORDER BY f.updated_at, f.id
{{> sql.paging}}
