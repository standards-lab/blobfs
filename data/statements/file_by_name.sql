--| tier: standard
-- The file of directory_id named name, whatever its status: Files.FindByName
-- and the lookup of Files.Ensure, which finds the pending row a retried
-- write resumes. (directory_id, name) is unique, so at most one row matches.
-- The caller normalizes the name first.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.directory_id = {{directory_id:uuid}} AND f.name = {{name}}
