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
-- transaction's check and its update. A new parent that does not exist
-- fails the foreign key blobfs_fk_directory_parent, and a name already
-- held under the new parent the unique constraint
-- blobfs_uq_directory_parent_name.
UPDATE blobfs_directory
SET parent_id = {{parent_id:uuid}},
    name = {{name}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND parent_id IS NOT NULL
