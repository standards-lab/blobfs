--| tier: standard
--| returning: file_by_id
-- The guarded update of a file move, leaving the key. It changes nothing
-- for a deleting file, a missing or deleting directory, or a name a
-- deleting file holds, rather than fail a constraint and abort the
-- caller's transaction; the store then reads why. Fails
-- blobfs_uq_file_directory_name for a live holder.
UPDATE blobfs_file
SET directory_id = {{directory_id:uuid}},
    name = {{name}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND status <> 'deleting'
  AND EXISTS (SELECT 1 FROM blobfs_directory s WHERE s.id = blobfs_file.directory_id AND s.status = 'active')
  AND EXISTS (SELECT 1 FROM blobfs_directory d WHERE d.id = {{directory_id:uuid}} AND d.status = 'active')
  AND NOT EXISTS (SELECT 1 FROM blobfs_file h WHERE h.directory_id = {{directory_id:uuid}} AND h.name = {{name}} AND h.status = 'deleting')
