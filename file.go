package blobfs

import "time"

// File is one file's metadata, a row of blobfs_file. Key is the object's
// key, built once by NewKey and never parsed back. Size and ETag are nil
// until the row is available, and ContentType is the declared type until
// the completion records the store's. CreatedAt and UpdatedAt are in
// time.UTC, whatever time.Local is. The json tags are the scan and binding
// contract.
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

// Object is what the store reports about a stored object, which the
// completion of a write records.
type Object struct {
	Size        int64
	ContentType string
	ETag        string
}
