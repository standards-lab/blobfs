--| tier: standard
-- One directory by id, and the read of the returning commands
-- create_directory and move_directory.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
WHERE d.id = {{id:uuid}}
