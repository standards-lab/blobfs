--| tier: standard
--| transaction: required
--| returning: file_by_id
-- The first step of the two-phase delete: moves the row to deleting,
-- advancing its version. The status predicate leaves a row already
-- deleting unchanged, so a retry advances nothing; the version is
-- nullable, and NULL guards nothing. A transaction is required so the row
-- lock, which waits on a hold, lasts until the caller commits.
UPDATE blobfs_file
SET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = {{id:uuid}} AND status <> 'deleting'
  AND ({{version:bigint}} IS NULL OR version = {{version:bigint}})
