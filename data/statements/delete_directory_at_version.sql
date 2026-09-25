--| tier: standard
-- delete_directory with the version the caller read in the predicate, for a
-- consumer that guards the removal of a directory it acts on by the version
-- its client last saw. A row at another version matches nothing, as a row
-- that does not exist does; the caller reads the row to tell the two apart.
-- The foreign keys refuse a directory that is not empty, as there.
DELETE FROM blobfs_directory
WHERE id = {{id:uuid}} AND parent_id IS NOT NULL AND version = {{version:bigint}}
