--| tier: standard
--| transaction: required
-- The baseline's hold: an update that assigns updated_at to itself takes
-- the row lock delete_file waits on and changes no value or version. A
-- deleting row matches nothing; the version is nullable, and NULL guards
-- nothing. A transaction is required, since autocommit would release the
-- lock at once.
UPDATE blobfs_file
SET updated_at = updated_at
WHERE id = {{id:uuid}} AND status <> 'deleting'
  AND ({{version:bigint}} IS NULL OR version = {{version:bigint}})
