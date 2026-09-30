--| tier: standard
--| transaction: required
--| returning: directory_by_id
-- The guarded update of a directory move. It changes nothing for the root,
-- a deleting directory, a missing or deleting parent, or a name a deleting
-- directory holds, rather than fail a constraint and abort the
-- transaction; the store then reads why. The current parent is correlated
-- by the table's own name, which the subquery's alias leaves unshadowed.
-- Fails blobfs_uq_directory_parent_name for a live holder. A transaction is
-- required: it runs after the tree lock and the cycle check.
UPDATE blobfs_directory
SET parent_id = {{parent_id:uuid}},
    name = {{name}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND parent_id IS NOT NULL AND status = 'active'
  AND EXISTS (SELECT 1 FROM blobfs_directory s WHERE s.id = blobfs_directory.parent_id AND s.status = 'active')
  AND EXISTS (SELECT 1 FROM blobfs_directory p WHERE p.id = {{parent_id:uuid}} AND p.status = 'active')
  AND NOT EXISTS (SELECT 1 FROM blobfs_directory h WHERE h.parent_id = {{parent_id:uuid}} AND h.name = {{name}} AND h.status = 'deleting')
