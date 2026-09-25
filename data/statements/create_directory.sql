--| tier: standard
--| returning: directory_by_id
-- Inserts a non-root directory under its parent. The row is selected from
-- the parent only while the parent is active, so a missing or deleting
-- parent inserts nothing. Fails blobfs_fk_directory_parent for a parent
-- removed after the select, blobfs_uq_directory_parent_name, and
-- blobfs_pk_directory.
INSERT INTO blobfs_directory (id, parent_id, name)
SELECT {{id:uuid}}, p.id, {{name}}
FROM blobfs_directory p
WHERE p.id = {{parent_id:uuid}} AND p.status = 'active'
