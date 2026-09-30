--| tier: standard
--| returning: file_by_id
-- The write's first step: inserts the row pending with its key and
-- declared content type. It selects nothing in a missing or deleting
-- directory, or for a name a deleting file holds, rather than fail a
-- constraint and abort the caller's transaction; the store then reads why.
-- Fails blobfs_fk_file_directory for a directory removed after the select,
-- blobfs_uq_file_directory_name for a live holder, and blobfs_pk_file.
INSERT INTO blobfs_file (id, directory_id, name, status, key, content_type)
SELECT {{id:uuid}}, d.id, {{name}}, 'pending', {{key}}, {{content_type}}
FROM blobfs_directory d
WHERE d.id = {{directory_id:uuid}} AND d.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM blobfs_file h WHERE h.directory_id = d.id AND h.name = {{name}} AND h.status = 'deleting')
