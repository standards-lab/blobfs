--| tier: native
--| native: SELECT ... FOR NO KEY UPDATE, the row lock an update of non-key columns takes, taken
--| [native]: without writing a row version. Port: an engine takes the lock a Files.Delete of the
--| [native]: row waits on, held until the transaction ends; SELECT ... FOR UPDATE where no weaker
--| [native]: row lock exists (MySQL, Oracle), SQL Server's UPDLOCK hint, or the baseline's
--| [native]: self-assigning update, the portable form, where no locking read exists (SQLite).
--| transaction: required
-- The hold of a file row for the rest of the caller's transaction, the
-- PostgreSQL variant's form of the baseline's hold_file: the library's half
-- of the reference-then-delete rule. FOR NO KEY UPDATE takes the same row
-- lock as the update delete_file runs, so the two wait on each other, and
-- it writes no row version: the row's xmin and ctid stay as they were and
-- the table gains no dead tuple, where the baseline's self-assigning update
-- writes one. It does not conflict with the FOR KEY SHARE lock a consumer's
-- foreign-key insert takes, so the consumer's reference inserts beside the
-- hold. A row that is deleting yields no row and takes no lock; under read
-- committed a hold that waited on a delete re-reads the row as the delete
-- left it, so it refuses a row whose delete committed meanwhile. The
-- variant reports a row returned as held, and the store reads the row to
-- classify a refusal. A transaction is required because a lock autocommit
-- releases at once holds nothing.
SELECT f.id
FROM blobfs_file f
WHERE f.id = {{id:uuid}} AND f.status <> 'deleting'
FOR NO KEY UPDATE
