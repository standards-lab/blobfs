DROP INDEX blobfs_ix_file_stale;

ALTER TABLE blobfs_directory DROP COLUMN status;
