--| tier: standard
-- One file by id: Files.Find, the read a hold or a purge that changed no row
-- classifies from, and the read the returning commands create_file,
-- complete_file, move_file, and delete_file name, so its select list is the
-- row both of their forms scan.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.id = {{id:uuid}}
