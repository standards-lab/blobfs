--| tier: standard
-- The last step of the two-phase delete: removes the row only while it is
-- deleting. blobfs owns no foreign key into blobfs_file, so a foreign-key
-- violation here is always a consumer's.
DELETE FROM blobfs_file
WHERE id = {{id:uuid}} AND status = 'deleting'
