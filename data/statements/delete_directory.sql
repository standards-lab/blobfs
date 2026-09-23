--| tier: standard
-- Removes one non-root directory. The root is refused in Go before this runs,
-- and the parent_id predicate keeps the statement itself from ever removing
-- it, so no statement of the library can remove the root. A directory that
-- still has child directories fails the foreign key
-- blobfs_fk_directory_parent and one that still has files
-- blobfs_fk_file_directory, which the delete mapping reports as ErrNotEmpty;
-- there is no cascade, and no version guard, since the keys refuse the one
-- case a stale version would catch. A consumer's foreign key into
-- blobfs_directory refuses the removal as ErrReferenced. No row affected
-- means the directory does not exist. It is one statement, so it runs on the
-- pool or in a caller's transaction alike.
DELETE FROM blobfs_directory
WHERE id = {{id:uuid}} AND parent_id IS NOT NULL
