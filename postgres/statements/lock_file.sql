--| tier: native
--| native: SELECT ... FOR NO KEY UPDATE, the row lock an update of non-key columns takes, taken
--| [native]: without writing a row version. Port: an engine takes the lock a Files.Delete of the
--| [native]: row waits on, held until the transaction ends; SELECT ... FOR UPDATE where no weaker
--| [native]: row lock exists (MySQL, Oracle), SQL Server's UPDLOCK hint, or the baseline's
--| [native]: self-assigning update, the portable form, where no locking read exists (SQLite).
--| [native]: The nullable version predicate is standard SQL.
--| transaction: required
-- The PostgreSQL form of hold_file: FOR NO KEY UPDATE takes the row lock
-- delete_file's update takes without writing a row version, and does not
-- conflict with the FOR KEY SHARE lock of a consumer's foreign-key insert.
-- A deleting row yields no row and takes no lock. A transaction is
-- required, since autocommit would release the lock at once.
SELECT f.id
FROM blobfs_file f
WHERE f.id = {{id:uuid}} AND f.status <> 'deleting'
  AND ({{version:bigint}} IS NULL OR f.version = {{version:bigint}})
FOR NO KEY UPDATE
