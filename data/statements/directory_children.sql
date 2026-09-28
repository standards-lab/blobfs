--| tier: standard
--| key: name
--| field: id uuid not null
--| field: parent_id uuid
--| field: name text not null
--| field: status text not null
--| field: version bigint not null
--| field: created_at timestamp with time zone not null
--| field: updated_at timestamp with time zone not null
-- The directories under one parent, whatever their status: the projection
-- base of the directory listing. (parent_id, name) is unique, so name is
-- the key. parent_id is declared nullable, as the schema declares it.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
WHERE d.parent_id = {{parent_id:uuid}}
