--| tier: standard
--| transaction: required
--| returning: file_by_id
-- The first step of a file delete: moves the row to deleting, advances its
-- version, stamps updated_at, and returns the row with the key the caller
-- deletes the object under. A row that is already deleting is left as it is,
-- and its read returns it unchanged, so a retry converges and the version
-- advances once per delete. It requires a transaction because it is the
-- delete's half of the reference-then-delete rule: the row lock this update
-- takes, or waits on behind a hold_file, holds until the caller commits, so
-- a consumer's reference to the file commits before this sees the row or
-- waits until the delete is decided, and the fallback's read sees the row as
-- this statement left it. A row that does not exist changes nothing and its
-- read finds no row.
UPDATE blobfs_file
SET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = {{id:uuid}} AND status <> 'deleting'
