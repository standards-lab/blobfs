--| tier: standard
--| transaction: required
-- The hold of a file row at the version the caller read, for a caller that
-- acts on a listing without reading the row again: hold_file with the
-- version in the predicate. As there, the update changes no value and
-- advances no version, so other holders of the version stay valid, and the
-- row's lock holds until the transaction ends. A row at another version, or
-- one that is deleting, matches nothing; the caller reads the row to tell
-- the two apart and from a row that does not exist.
UPDATE blobfs_file
SET updated_at = updated_at
WHERE id = {{id:uuid}} AND status <> 'deleting' AND version = {{version:bigint}}
