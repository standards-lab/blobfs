--| tier: standard
--| transaction: required
--| returning: directory_by_id
-- The guarded update of a directory move: sets parent_id and name at the
-- caller's version. The parent_id predicate keeps the root out. The status
-- predicates require the directory and its current and new parents to be
-- active; the current parent is correlated by the table's own name, which
-- the subquery's alias leaves unshadowed, and the new parent's predicate
-- refuses a missing parent before blobfs_fk_directory_parent could. A
-- deleting directory that holds the name under the new parent refuses the
-- move by the last predicate rather than by the unique constraint, which
-- would abort the transaction. Fails blobfs_uq_directory_parent_name for a
-- holder that is not deleting. A transaction is required: it runs after
-- the tree lock and the cycle check.
UPDATE blobfs_directory
SET parent_id = {{parent_id:uuid}},
    name = {{name}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND parent_id IS NOT NULL AND status = 'active'
  AND EXISTS (SELECT 1 FROM blobfs_directory s WHERE s.id = blobfs_directory.parent_id AND s.status = 'active')
  AND EXISTS (SELECT 1 FROM blobfs_directory p WHERE p.id = {{parent_id:uuid}} AND p.status = 'active')
  AND NOT EXISTS (SELECT 1 FROM blobfs_directory h WHERE h.parent_id = {{parent_id:uuid}} AND h.name = {{name}} AND h.status = 'deleting')
