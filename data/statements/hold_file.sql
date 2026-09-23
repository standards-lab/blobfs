--| tier: standard
--| transaction: required
-- The hold of a file row for the rest of the caller's transaction: the
-- library's half of the reference-then-delete rule. The update assigns
-- updated_at to itself, so it changes no value and advances no version, and
-- it takes the row's lock, which holds until the transaction ends. A
-- delete_file that runs meanwhile waits on that lock and, once the caller's
-- row that references the file has committed, sees it. A row that is
-- deleting matches nothing, so the caller refuses to reference a file whose
-- delete has begun; the caller reads the row to tell that from a row that
-- does not exist. A transaction is required because a lock autocommit
-- releases at once holds nothing.
UPDATE blobfs_file
SET updated_at = updated_at
WHERE id = {{id:uuid}} AND status <> 'deleting'
