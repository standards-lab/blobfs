-- File metadata. Every file sits in a directory, and the foreign key from
-- directory_id has no cascading action: a directory that still has files
-- refuses its delete through blobfs_fk_file_directory. Size and etag stay
-- NULL until the object exists; content_type is declared at upload. A
-- deleting row keeps its (directory_id, name) slot until it is removed.
CREATE TABLE blobfs_file (
  id uuid NOT NULL,
  directory_id uuid NOT NULL,
  name text NOT NULL,
  status text NOT NULL,
  key text NOT NULL,
  size bigint,
  content_type text NOT NULL,
  etag text,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT blobfs_pk_file PRIMARY KEY (id),
  CONSTRAINT blobfs_fk_file_directory FOREIGN KEY (directory_id) REFERENCES blobfs_directory (id),
  CONSTRAINT blobfs_uq_file_directory_name UNIQUE (directory_id, name),
  CONSTRAINT blobfs_cc_file_name CHECK (name <> ''),
  CONSTRAINT blobfs_cc_file_status CHECK (status IN ('pending', 'available', 'deleting'))
);
