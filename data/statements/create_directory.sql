--| tier: standard
--| returning: directory_by_id
-- Inserts a non-root directory. It selects nothing under a missing or
-- deleting parent, or for a name a deleting directory holds, rather than
-- fail a constraint and abort the caller's transaction; the store then
-- reads why. Fails blobfs_fk_directory_parent for a parent removed after
-- the select, blobfs_uq_directory_parent_name for a live holder, and
-- blobfs_pk_directory.
INSERT INTO blobfs_directory (id, parent_id, name)
SELECT {{id:uuid}}, p.id, {{name}}
FROM blobfs_directory p
WHERE p.id = {{parent_id:uuid}} AND p.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM blobfs_directory h WHERE h.parent_id = p.id AND h.name = {{name}} AND h.status = 'deleting')
