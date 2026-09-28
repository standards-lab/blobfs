--| tier: standard
-- Removes one directory, at the caller's version when one is bound; the
-- version is nullable, and NULL guards nothing. The parent_id predicate
-- keeps the root out. Fails blobfs_fk_directory_parent and
-- blobfs_fk_file_directory while the directory has children or files, and
-- any consumer's foreign key into blobfs_directory.
DELETE FROM blobfs_directory
WHERE id = {{id:uuid}} AND parent_id IS NOT NULL
  AND ({{version:bigint}} IS NULL OR version = {{version:bigint}})
