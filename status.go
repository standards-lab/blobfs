package blobfs

import "fmt"

// Status is a file row's place in the two-phase write and delete: the row
// is inserted pending before the object is written, becomes available once
// the object exists, and is marked deleting before the object is removed.
// Its underlying type is string, so database/sql binds and scans it as the
// status column's text without a Valuer or Scanner.
type Status string

const (
	// StatusPending marks a row whose object has not been written yet. A
	// crash after the insert leaves the row pending, where a query finds it.
	StatusPending Status = "pending"

	// StatusAvailable marks a row whose object exists in the store.
	StatusAvailable Status = "available"

	// StatusDeleting marks a row whose object is being removed. The row
	// keeps its name until the purge removes it.
	StatusDeleting Status = "deleting"
)

func (s Status) String() string {
	return string(s)
}

// Valid reports whether s is one of the three statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusAvailable, StatusDeleting:
		return true
	}
	return false
}

// Mutable reports whether a row in status s accepts a move or a rename. A
// deleting row refuses both, so a concurrent move cannot relocate a row
// whose object is gone. The write and delete steps are status transitions
// and are governed by CanTransition instead.
func (s Status) Mutable() bool {
	return s != StatusDeleting
}

// transitions is the table of allowed status changes. Completing a write
// moves pending to available. Beginning a delete moves pending or available
// to deleting, and deleting to deleting again, which keeps the delete's
// first step idempotent under retry. No transition leaves deleting except
// the row's removal, which is not a status.
//
// The table has no failed status. A write that stops after the pending row
// is inserted leaves the row pending, where a query finds it and a retry
// of the same write completes it, and a write that is abandoned is removed
// through the delete steps, which pending already allows. A fourth status
// would name a state the delete steps already handle.
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

// CanTransition reports whether a row may move from one status to another.
// A change the table does not list is refused, including every change out
// of deleting and every change between the same non-deleting status.
func CanTransition(from, to Status) bool {
	return transitions[from][to]
}

// Transition returns nil when the change from one status to another is
// allowed and a TransitionError otherwise.
func Transition(from, to Status) error {
	if CanTransition(from, to) {
		return nil
	}
	return &TransitionError{From: from, To: to}
}

// TransitionError reports a refused status change. It matches
// ErrInvalidTransition under errors.Is, and also ErrDeleting when the row
// was deleting, so a caller can tell a delete in progress from any other
// refusal without inspecting the fields.
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
