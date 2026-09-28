--| tier: native
--| native: SELECT ... FOR NO KEY UPDATE, the row lock an update of non-key columns takes, taken
--| [native]: without writing a row version. Port: an engine takes the lock a Files.Delete of the
--| [native]: row waits on, held until the transaction ends; SELECT ... FOR UPDATE where no weaker
--| [native]: row lock exists (MySQL, Oracle), SQL Server's UPDLOCK hint, or the baseline's
--| [native]: self-assigning update, the portable form, where no locking read exists (SQLite).
--| [native]: The nullable version predicate is standard SQL.
--| transaction: required
-- The PostgreSQL form of hold_file: FOR NO KEY UPDATE takes the row lock
-- delete_file's update takes, and writes no row version. It does not
-- conflict with the FOR KEY SHARE lock a consumer's foreign-key insert
-- takes, so the consumer's reference inserts beside the hold. A deleting
-- row yields no row and takes no lock; under read committed a hold that
-- waited on a delete re-reads the row as the delete left it. The version
-- is nullable, and NULL guards nothing. A transaction is required, since
-- autocommit would release the lock at once.
SELECT f.id
FROM blobfs_file f
WHERE f.id = {{id:uuid}} AND f.status <> 'deleting'
  AND ({{version:bigint}} IS NULL OR f.version = {{version:bigint}})
FOR NO KEY UPDATE
