--| tier: native
--| native: SELECT ... FOR NO KEY UPDATE, the row lock an update of non-key columns takes, taken
--| [native]: without writing a row version. Port: as lock_file; the version predicate is
--| [native]: standard SQL.
--| transaction: required
-- The hold of a file row at the version the caller read, for a caller that
-- acts on a listing without reading the row again: lock_file with the
-- version in the predicate, the PostgreSQL variant's form of the baseline's
-- hold_file_at_version. As there, the read writes no row version and takes
-- the lock delete_file waits on. A row at another version, or one that is
-- deleting, yields no row and takes no lock; the store reads the row to
-- tell the two apart and from a row that does not exist.
SELECT f.id
FROM blobfs_file f
WHERE f.id = {{id:uuid}} AND f.status <> 'deleting' AND f.version = {{version:bigint}}
FOR NO KEY UPDATE
