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
-- of Files.List and Files.Continue, anchored on directory_id. The library
-- wraps it as a derived table and composes the caller's filters, sort,
-- total, and page outside it. name is the key: (directory_id, name) is
-- unique, so the default order is by name and every sort is total. size
-- and etag stay null until a write completes, so a sort by either pages
-- by number only. key is not declared: it is derived from the id and the
-- name, and no listing filters or sorts by it.
SELECT {{> blobfs.file_columns}}
FROM blobfs_file f
WHERE f.directory_id = {{directory_id:uuid}}
