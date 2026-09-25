--| tier: standard
--| returning: directory_by_id
-- Inserts a non-root directory under its parent and returns the row. The id
-- is minted in Go or supplied by the caller; status, version, and the
-- timestamps take the table's defaults, which the returning command hands
-- back: in the single-statement form on an engine whose dialect renders
-- RETURNING, and elsewhere in the fallback, this insert followed by
-- directory_by_id in one transaction. The row is selected from its parent,
-- and only from a parent that is active, so a parent that is missing or
-- deleting inserts nothing and the read finds no row; the caller reads the
-- parent to tell the two apart. A parent removed after the select still
-- fails the foreign key blobfs_fk_directory_parent, a taken name fails the
-- unique constraint blobfs_uq_directory_parent_name, and a taken id the
-- primary key blobfs_pk_directory.
INSERT INTO blobfs_directory (id, parent_id, name)
SELECT {{id:uuid}}, p.id, {{name}}
FROM blobfs_directory p
WHERE p.id = {{parent_id:uuid}} AND p.status = 'active'
