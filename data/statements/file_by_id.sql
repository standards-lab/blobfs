--| tier: standard
-- One file by id, and the read of the returning commands create_file,
-- complete_file, move_file, and delete_file.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.id = {{id:uuid}}
