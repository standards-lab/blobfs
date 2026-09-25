--| tier: standard
--| returning: file_by_id
-- The first step of the two-phase write: inserts the row as pending with
-- its key and declared content type. The row is selected from the
-- directory only while the directory is active, so a missing or deleting
-- directory inserts nothing. Fails blobfs_fk_file_directory for a
-- directory removed after the select, blobfs_uq_file_directory_name, and
-- blobfs_pk_file.
INSERT INTO blobfs_file (id, directory_id, name, status, key, content_type)
SELECT {{id:uuid}}, d.id, {{name}}, 'pending', {{key}}, {{content_type}}
FROM blobfs_directory d
WHERE d.id = {{directory_id:uuid}} AND d.status = 'active'
