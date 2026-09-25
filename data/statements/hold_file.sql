--| tier: standard
--| transaction: required
-- The hold of a file row for the rest of the caller's transaction, the
-- baseline's form of the variation point Variant.HoldFile: the library's
-- half of the reference-then-delete rule. The update assigns
-- updated_at to itself: it changes no value and advances no version, so
-- other holders of the version stay valid, and it takes the row's lock,
-- which holds until the transaction ends. A delete_file that runs meanwhile
-- waits on that lock and, once the caller's row that references the file has
-- committed, sees it. A row that is deleting matches nothing, so the caller
-- refuses to reference a file whose delete has begun. The version is
-- nullable: NULL, the default, guards nothing, and a version the caller read
-- from a listing, for a caller that acts on it without reading the row
-- again, makes a row at another version match nothing too. The caller reads
-- the row to tell a refusal from a row that does not exist. A transaction is
-- required because a lock autocommit releases at once holds nothing.
UPDATE blobfs_file
SET updated_at = updated_at
WHERE id = {{id:uuid}} AND status <> 'deleting'
  AND ({{version:bigint}} IS NULL OR version = {{version:bigint}})
