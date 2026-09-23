--| tier: standard
-- One directory by id: Directories.Find, the first read of a path walk,
-- and the read the returning commands create_directory and move_directory
-- name, so its select list is the row both of their forms scan.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
WHERE d.id = {{id:uuid}}
