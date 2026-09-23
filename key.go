package blobfs

import (
	"fmt"
	"strings"
	"unicode"
)

// KeyValidator is what blobfs asks of the object store: whether it accepts
// a key. A consumer wires its store's key validation to this interface at
// its composition root, so blobfs validates a key before it stores the key
// in a pending row and never imports the store's package.
//
// The interface carries no maximum key length. The store's own validation
// enforces its limit, counted the way the store counts, and MaxNameLength
// keeps every key blobfs builds far below the limits of the providers it
// targets (a key of the longest name is 292 runes), so a second length
// check in blobfs never fires against a real store.
type KeyValidator interface {
	// ValidateKey returns a non-nil error that says why when the store
	// refuses key.
	ValidateKey(key string) error
}

// fallbackSegment stands in for a name the sanitizer emptied out, such as a
// name made only of dots or whitespace.
const fallbackSegment = "file"

// SanitizeFilename turns a display name into the filename segment of a key.
// The segment is frozen when the pending row is inserted, is never kept in
// step with a later rename, and is never parsed back out of the key. It
// exists so an operator browsing the raw container can read it.
//
// The result satisfies the key rules of the providers blobfs targets: each
// invalid UTF-8 sequence, slash, backslash, and control character becomes
// an underscore, trailing dots and whitespace are removed, and a name that
// nothing survives of becomes "file".
func SanitizeFilename(name string) string {
	s := strings.ToValidUTF8(name, "_")
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return r == '.' || unicode.IsSpace(r)
	})
	if s == "" {
		return fallbackSegment
	}
	return s
}

// NewKey builds a file's key from its id and display name and validates it
// against the store. The key is id, a slash, and SanitizeFilename(name). A
// refusal is a KeyError carrying the store's reason.
func NewKey(store KeyValidator, id, name string) (string, error) {
	key := id + "/" + SanitizeFilename(name)
	if err := store.ValidateKey(key); err != nil {
		return "", &KeyError{Key: key, Err: err}
	}
	return key, nil
}

// KeyError reports a key the store refused, with the store's reason. It
// matches ErrInvalidKey under errors.Is, and errors.As reaches the store's
// own error through Unwrap.
type KeyError struct {
	Key string
	Err error
}

func (e *KeyError) Error() string {
	return fmt.Sprintf("blobfs: invalid key %q: %v", e.Key, e.Err)
}

// Unwrap returns ErrInvalidKey and the store's error, so errors.Is matches
// the sentinel and errors.As finds the cause.
func (e *KeyError) Unwrap() []error {
	return []error{ErrInvalidKey, e.Err}
}
