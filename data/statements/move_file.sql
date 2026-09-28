--| tier: standard
--| returning: file_by_id
-- The guarded update of a file move: sets directory_id and name at the
-- caller's version, leaving the key. The status predicates require the
-- file not to be deleting and its current and new directories to be
-- active; the new directory's predicate refuses a missing directory before
-- blobfs_fk_file_directory could. Fails blobfs_uq_file_directory_name.
UPDATE blobfs_file
SET directory_id = {{directory_id:uuid}},
    name = {{name}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND status <> 'deleting'
  AND EXISTS (SELECT 1 FROM blobfs_directory s WHERE s.id = blobfs_file.directory_id AND s.status = 'active')
  AND EXISTS (SELECT 1 FROM blobfs_directory d WHERE d.id = {{directory_id:uuid}} AND d.status = 'active')
