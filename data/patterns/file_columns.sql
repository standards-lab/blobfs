--| tier: standard
-- The columns of a file row in the order blobfs.File scans them, over the
-- correlation name f: the including statement reads FROM blobfs_file f.
f.id, f.directory_id, f.name, f.status, f.key, f.size, f.content_type, f.etag, f.version, f.created_at, f.updated_at
