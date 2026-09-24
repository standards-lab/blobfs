package blobfs

import "time"

// File is one file's metadata, a row of blobfs_file. DirectoryID is never
// nil: every file sits in a directory. Key is the object's key in the store,
// built once by NewKey when the pending row is inserted and never parsed
// back. Size and ETag are nil until the row is available, because the store
// reports them only once the object exists; ContentType is the media type
// the consumer declares at upload, so it is known from the pending row on,
// and completing the write replaces it with the type the store reports.
// Version is the concurrency token the guarded commands check. The json
// tags are the scan and binding contract: the columns carry the same names.
type File struct {
	ID          string    `json:"id"`
	DirectoryID string    `json:"directory_id"`
	Name        string    `json:"name"`
	Status      Status    `json:"status"`
	Key         string    `json:"key"`
	Size        *int64    `json:"size"`
	ContentType string    `json:"content_type"`
	ETag        *string   `json:"etag"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Object is what the store reports about a stored object once a write has
// completed: the facts go-storage's Object carries that a file row keeps.
// The consumer builds one from the store's answer to its put and hands it
// to the step that completes the write, so the row records the size the
// store counted, the entity tag it assigned, and the content type it
// holds.
type Object struct {
	Size        int64
	ContentType string
	ETag        string
}
