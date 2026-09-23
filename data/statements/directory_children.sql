--| tier: standard
--| key: name
--| field: id uuid not null
--| field: parent_id uuid
--| field: name text not null
--| field: version bigint not null
--| field: created_at timestamp with time zone not null
--| field: updated_at timestamp with time zone not null
-- The directories under one parent: the projection base of
-- Directories.List and Directories.Continue, anchored on parent_id. The
-- library wraps it as a derived table and composes the caller's filters,
-- sort, total, and page outside it. name is the key: (parent_id, name) is
-- unique, so the default order is by name and every sort is total. The
-- root has no parent, so it never appears in a listing; the listing of
-- blobfs.RootID is the depth-one directories. parent_id is declared as the
-- schema declares it, nullable, so a sort by it pages by number only.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
WHERE d.parent_id = {{parent_id:uuid}}
