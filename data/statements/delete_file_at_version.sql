--| tier: standard
--| transaction: required
--| returning: file_by_id
-- delete_file with the version the caller read in the predicate: the row
-- moves to deleting only at that version. A row that is already deleting is
-- left as it is and returned, so a retry of a delete that began converges
-- whatever version it was guarded by; a row at another version is returned
-- unchanged and not deleting, which the caller reports as a stale version.
UPDATE blobfs_file
SET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = {{id:uuid}} AND status <> 'deleting' AND version = {{version:bigint}}
