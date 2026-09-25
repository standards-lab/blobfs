--| tier: standard
--| returning: file_by_id
-- The first step of the two-phase write: inserts the row as pending, before
-- the object exists, with the key the object will be stored under and the
-- content type the caller declared, and returns the row. The id is minted in
-- Go or supplied by the caller, and the key is built from it and validated
-- against the consumer's store before this runs. size and etag stay NULL
-- until the write completes; version and the timestamps take the table's
-- defaults, which the returning command hands back: in the single-statement
-- form on an engine whose dialect renders RETURNING, and elsewhere in the
-- fallback, this insert followed by file_by_id in one transaction. The row
-- is selected from its directory, and only from a directory that is active,
-- so a directory that is missing or deleting inserts nothing and the read
-- finds no row; the caller reads the directory to tell the two apart. A
-- directory removed after the select still fails the foreign key
-- blobfs_fk_file_directory, a taken name fails the unique constraint
-- blobfs_uq_file_directory_name, and a taken id the primary key
-- blobfs_pk_file.
INSERT INTO blobfs_file (id, directory_id, name, status, key, content_type)
SELECT {{id:uuid}}, d.id, {{name}}, 'pending', {{key}}, {{content_type}}
FROM blobfs_directory d
WHERE d.id = {{directory_id:uuid}} AND d.status = 'active'
