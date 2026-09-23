--| tier: standard
--| returning: directory_by_id
-- Inserts a non-root directory under its parent and returns the row. The id
-- is minted in Go or supplied by the caller; version and the timestamps take
-- the table's defaults, which the returning command hands back: in the
-- single-statement form on an engine whose dialect renders RETURNING, and
-- elsewhere in the fallback, this insert followed by directory_by_id in one
-- transaction. A missing parent fails the foreign key
-- blobfs_fk_directory_parent, a taken name the unique constraint
-- blobfs_uq_directory_parent_name, and a taken id the primary key
-- blobfs_pk_directory.
INSERT INTO blobfs_directory (id, parent_id, name)
VALUES ({{id:uuid}}, {{parent_id:uuid}}, {{name}})
