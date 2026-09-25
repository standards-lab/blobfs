-- A directory's place in the delete of a branch. Every directory is active
-- until Directories.MarkDeleting marks it, with every directory and file
-- beneath it, deleting; a deleting directory accepts no new child, no file,
-- and no move in or out, and the mark is never undone, since the branch's
-- objects are being removed. Existing rows take the default and stay active.
-- The check blobfs_cc_directory_status names the two statuses, as
-- blobfs_cc_file_status names a file's. This migration is one statement.
ALTER TABLE blobfs_directory
  ADD COLUMN status text NOT NULL DEFAULT 'active',
  ADD CONSTRAINT blobfs_cc_directory_status CHECK (status IN ('active', 'deleting'));
