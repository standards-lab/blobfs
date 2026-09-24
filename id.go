package blobfs

import (
	"fmt"
	"uuid"
)

// NewID mints a new row id: a version 7 UUID in its canonical string form,
// so ids sort in insertion order and bind to a uuid column as text.
func NewID() string {
	return uuid.NewV7().String()
}

// ParseID checks a row id a caller supplies in place of a minted one and
// returns it in the canonical form NewID mints, so the row and the key
// built from its id carry the same text whatever form the caller wrote.
// Any form uuid.Parse accepts is taken. The nil UUID is refused, because
// it is RootID and belongs to the seeded root. A refusal is an IDError.
func ParseID(id string) (string, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return "", &IDError{ID: id, Reason: "must be a UUID"}
	}
	if u == uuid.Nil() {
		return "", &IDError{ID: id, Reason: "the nil UUID is the root's"}
	}
	return u.String(), nil
}

// IDError reports a row id that ParseID refused, with the reason. It
// matches ErrInvalidID under errors.Is.
type IDError struct {
	ID     string
	Reason string
}

func (e *IDError) Error() string {
	return fmt.Sprintf("blobfs: invalid id %q: %s", e.ID, e.Reason)
}

// Unwrap returns ErrInvalidID, so errors.Is matches the sentinel.
func (e *IDError) Unwrap() error {
	return ErrInvalidID
}
