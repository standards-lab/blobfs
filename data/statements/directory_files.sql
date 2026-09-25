--| tier: standard
--| key: name
--| field: id uuid not null
--| field: directory_id uuid not null
--| field: name text not null
--| field: status text not null
--| field: size bigint
--| field: content_type text not null
--| field: etag text
--| field: version bigint not null
--| field: created_at timestamp with time zone not null
--| field: updated_at timestamp with time zone not null
-- The files in one directory, whatever their status: the projection base
-- of the file listing. (directory_id, name) is unique, so name is the key.
-- key is not declared: no listing filters or sorts by it.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.directory_id = {{directory_id:uuid}}
