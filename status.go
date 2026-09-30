package blobfs

import "fmt"

// Status is a file row's place in the two-phase write and delete: pending
// until its object exists, then available, and deleting once its delete
// begins. It binds and scans as the status column's text.
type Status string

const (
	// StatusPending marks a row whose object has not been written yet.
	StatusPending Status = "pending"

	// StatusAvailable marks a row whose object exists in the store.
	StatusAvailable Status = "available"

	// StatusDeleting marks a row whose object is being removed. The row
	// keeps its name until it is purged.
	StatusDeleting Status = "deleting"
)

func (s Status) String() string {
	return string(s)
}

// Mutable reports whether a row in status s accepts a move or a rename:
// every status but deleting. See Deleting outranks the version in
// docs/concepts.md.
func (s Status) Mutable() bool {
	return s != StatusDeleting
}

// transitions is the table of allowed status changes, the one in
// docs/features.md. Deleting to deleting is the delete's retry; the table
// has no failed status, since the delete steps remove an abandoned write.
var transitions = map[Status]map[Status]bool{
	StatusPending: {
		StatusAvailable: true,
		StatusDeleting:  true,
	},
	StatusAvailable: {
		StatusDeleting: true,
	},
	StatusDeleting: {
		StatusDeleting: true,
	},
}

// Transition returns nil when the transition table allows the change from
// one status to another and a TransitionError otherwise.
func Transition(from, to Status) error {
	if transitions[from][to] {
		return nil
	}
	return &TransitionError{From: from, To: to}
}

// TransitionError reports a refused status change. It matches
// ErrInvalidTransition, and ErrDeleting too when From is deleting.
type TransitionError struct {
	From Status
	To   Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("blobfs: status cannot change from %s to %s", e.From, e.To)
}

// Is reports whether e stands for target: ErrInvalidTransition always, and
// ErrDeleting when the refused change started from a deleting row.
func (e *TransitionError) Is(target error) bool {
	switch target {
	case ErrInvalidTransition:
		return true
	case ErrDeleting:
		return e.From == StatusDeleting
	}
	return false
}

// DirectoryStatus is a directory row's status: active until the delete of
// its branch marks it deleting, which nothing undoes. It binds and scans
// as the status column's text. See Deleting a branch in docs/concepts.md.
type DirectoryStatus string

const (
	// DirectoryStatusActive marks a directory that accepts children, files,
	// and moves: every directory, the root included, until a mark.
	DirectoryStatusActive DirectoryStatus = "active"

	// DirectoryStatusDeleting marks a directory whose branch is being
	// removed. Nothing enters or leaves it, and its rows keep their names
	// until they are removed.
	DirectoryStatusDeleting DirectoryStatus = "deleting"
)

func (s DirectoryStatus) String() string {
	return string(s)
}

// Mutable reports whether a directory in status s accepts a create or a
// move beneath it, and a move of itself or of anything in it: every
// status but deleting.
func (s DirectoryStatus) Mutable() bool {
	return s != DirectoryStatusDeleting
}
