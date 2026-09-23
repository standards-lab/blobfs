--| tier: standard
--| returning: file_by_id
-- A file move or rename, as the guarded command of the query library's
-- optimistic-concurrency protocol: sets the file's directory and name,
-- advances the version, stamps updated_at, and returns the row as it stands
-- afterward. The key is untouched: it was built from the id and the name at
-- the insert and the object stays under it, so a rename moves no object. The
-- guard's predicate names the id and the version the caller read; the status
-- predicate keeps a deleting row unchanged, since its object is being
-- removed, and the row its read returns at the expected version tells that
-- refusal from a version conflict. A directory that does not exist fails the
-- foreign key blobfs_fk_file_directory, and a name already held in the
-- directory the unique constraint blobfs_uq_file_directory_name. A file
-- cannot form a cycle, so no lock and no check precede it, and the session
-- may be the pool or a transaction.
UPDATE blobfs_file
SET directory_id = {{directory_id:uuid}},
    name = {{name}},
    {{> sql.guard_set}}
WHERE {{> sql.guard_where}} AND status <> 'deleting'
