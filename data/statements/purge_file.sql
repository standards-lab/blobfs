--| tier: standard
-- The last step of the two-phase delete: removes the row, and only a row that
-- is deleting, so a row whose delete has not begun is left as it is and the
-- caller reads it again to tell that refusal from a row that is already gone.
-- It is one statement, so it runs on the pool or in a caller's transaction
-- alike. blobfs owns no foreign key into blobfs_file, so a foreign-key
-- violation here is always a consumer's row that still references the file,
-- which the delete mapping reports as ErrReferenced with the constraint's
-- name reachable.
DELETE FROM blobfs_file
WHERE id = {{id:uuid}} AND status = 'deleting'
