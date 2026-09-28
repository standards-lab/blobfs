--| tier: standard
-- The child of parent_id named name, normalized by the caller.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
WHERE d.parent_id = {{parent_id:uuid}} AND d.name = {{name}}
