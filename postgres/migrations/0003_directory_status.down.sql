-- The down forgets which directories are deleting: every branch a mark
-- reached, swept partway or not at all, is an active subtree again, while
-- the files the mark reached stay deleting. Each object the up added is
-- dropped by name, the column last.
DROP INDEX blobfs_ix_file_stale;

DROP INDEX blobfs_ix_directory_deleting;

ALTER TABLE blobfs_directory DROP CONSTRAINT blobfs_cc_directory_status;

ALTER TABLE blobfs_directory DROP COLUMN status;
