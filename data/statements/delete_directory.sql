--| tier: standard
-- Removes one non-root directory, at the version the caller read when it
-- gives one. The root is refused in Go before this runs, and the parent_id
-- predicate keeps the statement itself from ever removing it, so no
-- statement of the library can remove the root. A directory that still has
-- child directories fails the foreign key blobfs_fk_directory_parent and one
-- that still has files blobfs_fk_file_directory, which the delete mapping
-- reports as ErrNotEmpty; there is no cascade. A consumer's foreign key into
-- blobfs_directory refuses the removal as ErrReferenced. The version is
-- nullable: NULL, the default, guards nothing, since the keys refuse the one
-- case a stale version would catch; a version, for a consumer that guards the
-- removal by the version its client last saw, makes a row at another version
-- match nothing, as a row that does not exist does, and the caller reads the
-- row to tell the two apart. No row affected without a version means the
-- directory does not exist. It is one statement, so it runs on the pool or in
-- a caller's transaction alike.
DELETE FROM blobfs_directory
WHERE id = {{id:uuid}} AND parent_id IS NOT NULL
  AND ({{version:bigint}} IS NULL OR version = {{version:bigint}})
