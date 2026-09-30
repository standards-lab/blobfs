package blobfs

import "time"

// RootID is the id of the one root directory of an install: the nil UUID,
// seeded by the directory migration. Every path starts at the root, and a
// consumer that needs the root reads it by this id instead of searching
// for the row with no parent.
const RootID = "00000000-0000-0000-0000-000000000000"

// Directory is one node of the tree, a row of blobfs_directory. The root
// alone has a nil ParentID and the Name "/". Version is the concurrency
// token the guarded commands check. The json tags are the scan and binding
// contract.
type Directory struct {
	ID        string          `json:"id"`
	ParentID  *string         `json:"parent_id"`
	Name      string          `json:"name"`
	Status    DirectoryStatus `json:"status"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// IsRoot reports whether d is the root directory: the row with no parent.
func (d Directory) IsRoot() bool {
	return d.ParentID == nil
}
