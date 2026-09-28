--| tier: standard
--| returning: file_by_id
-- The last step of the two-phase write: moves the pending row at the
-- caller's version to available with what the store reported. The status
-- predicate leaves a row completed already or deleting unchanged.
UPDATE blobfs_file
SET status = 'available',
    size = {{size:bigint}},
    content_type = {{content_type}},
    etag = {{etag}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND status = 'pending'
