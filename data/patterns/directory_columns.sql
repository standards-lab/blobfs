--| tier: standard
-- The columns of a directory row in the order blobfs.Directory scans them,
-- over the correlation name d: the including statement reads FROM
-- blobfs_directory d.
d.id, d.parent_id, d.name, d.version, d.created_at, d.updated_at
