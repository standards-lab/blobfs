--| tier: standard
-- The child of parent_id named name: Directories.FindByName, the lookup of
-- Directories.Ensure, and one step of the baseline's path walk. The caller
-- normalizes the name first.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
WHERE d.parent_id = {{parent_id:uuid}} AND d.name = {{name}}
