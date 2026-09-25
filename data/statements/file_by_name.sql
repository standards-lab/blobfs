--| tier: standard
-- The file of directory_id named name, normalized by the caller, whatever
-- its status.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.directory_id = {{directory_id:uuid}} AND f.name = {{name}}
