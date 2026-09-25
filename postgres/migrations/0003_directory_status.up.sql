-- A directory's place in the delete of a branch. Every directory is active
-- until Directories.MarkDeleting marks it, with every directory and file
-- beneath it, deleting; a deleting directory accepts no new child, no file,
-- and no move in or out, and the mark is never undone, since the branch's
-- objects are being removed. Existing rows take the default and stay active.
-- The check blobfs_cc_directory_status names the two statuses, as
-- blobfs_cc_file_status names a file's.
--
-- The partial index blobfs_ix_directory_deleting holds the deleting
-- directories alone, so the read of the branches being deleted, which a
-- sweeper repeats, costs the deleting rows and not the table; an active
-- directory, nearly every row, is not in it.
ALTER TABLE blobfs_directory
  ADD COLUMN status text NOT NULL DEFAULT 'active',
  ADD CONSTRAINT blobfs_cc_directory_status CHECK (status IN ('active', 'deleting'));

CREATE INDEX blobfs_ix_directory_deleting ON blobfs_directory (id) WHERE status = 'deleting';
