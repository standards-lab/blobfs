--| tier: standard
--| transaction: required
--| returning: directory_by_id
-- The last step of a directory move, as the guarded command of the query
-- library's optimistic-concurrency protocol: sets the directory's parent
-- and name, advances the version, and stamps updated_at, and returns the
-- row as it stands afterward. The guard's predicate names the id and the
-- version the caller read. The parent_id predicate keeps the statement
-- from ever moving the root, which Go refuses before this runs. It
-- requires a transaction because it is the third of three statements that
-- must see one tree lock: the lock, the cycle check, and this update, in
-- that order, so a concurrent move cannot pass its own check between this
-- transaction's check and its update.
--
-- The status predicates keep a deleting branch closed: the directory, its
-- current parent, and its new parent must each be active, so nothing moves
-- out of a branch marked for removal and nothing moves into one. Each
-- parent is read through the primary key, the current one correlated by
-- the table's own name, which the subquery's alias leaves unshadowed. When
-- nothing changed, the row its read returns tells a missing directory, a
-- version conflict, and a refusal apart, and the caller reads the two
-- parents to classify the refusal: a deleting one, or a new parent that
-- does not exist, which the predicate refuses before the foreign key
-- blobfs_fk_directory_parent could. A name already held under the new
-- parent fails the unique constraint blobfs_uq_directory_parent_name.
UPDATE blobfs_directory
SET parent_id = {{parent_id:uuid}},
    name = {{name}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND parent_id IS NOT NULL AND status = 'active'
  AND EXISTS (SELECT 1 FROM blobfs_directory s WHERE s.id = blobfs_directory.parent_id AND s.status = 'active')
  AND EXISTS (SELECT 1 FROM blobfs_directory p WHERE p.id = {{parent_id:uuid}} AND p.status = 'active')
