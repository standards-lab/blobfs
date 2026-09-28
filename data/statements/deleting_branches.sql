--| tier: standard
-- The roots of the branches being deleted: each deleting directory under
-- an active parent, in id order, one page past offset. The status
-- predicate is the one the postgres migrations' partial index
-- blobfs_ix_directory_deleting covers.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
JOIN blobfs_directory p ON p.id = d.parent_id
WHERE d.status = 'deleting' AND p.status = 'active'
ORDER BY d.id
{{> sql.paging}}
