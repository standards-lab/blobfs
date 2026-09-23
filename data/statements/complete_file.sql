--| tier: standard
--| returning: file_by_id
-- The last step of a file write, as the guarded command of the query
-- library's optimistic-concurrency protocol: moves the pending row to
-- available with the size, content type, and entity tag the store reported,
-- advances the version, stamps updated_at, and returns the row as it stands
-- afterward. The guard's predicate names the id and the version the caller
-- read from the pending row; the status predicate keeps a row that is no
-- longer pending, already completed or deleting, unchanged. When nothing
-- changed, the row its read returns tells a missing row, a version conflict,
-- and a refusal of the status predicate apart, so no second read runs.
UPDATE blobfs_file
SET status = 'available',
    size = {{size:bigint}},
    content_type = {{content_type}},
    etag = {{etag}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND status = 'pending'
