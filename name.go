package blobfs

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxNameLength is the longest name a directory or file may carry,
// counted in runes. It is the per-component limit the common file systems
// share (ext4, NTFS, and APFS all stop at 255), so a name copied from a
// local disk always fits. A key built from the longest name is 292 runes,
// well under the 1024 that Azure Blob Storage and S3 accept.
const MaxNameLength = 255

// NormalizeName returns name in Unicode normalization form C. Uniqueness is
// an exact match on the stored name, so the persistence layer normalizes
// every name before an insert or a rename: a composed and a decomposed
// spelling of the same name then collide instead of coexisting.
func NormalizeName(name string) string {
	return norm.NFC.String(name)
}

// ValidateName reports whether name may be a directory or file
// name. A name is non-empty, valid UTF-8, at most MaxNameLength runes,
// contains no slash and no control character, and is neither "." nor "..".
// The rejection is a NameError. Validate the normalized form: the check
// counts runes, and normalization can change the count.
func ValidateName(name string) error {
	switch {
	case name == "":
		return &NameError{Name: name, Reason: "must not be empty"}
	case !utf8.ValidString(name):
		return &NameError{Name: name, Reason: "must be valid UTF-8"}
	case name == "." || name == "..":
		return &NameError{Name: name, Reason: "must not be . or .."}
	case strings.ContainsRune(name, '/'):
		return &NameError{Name: name, Reason: "must not contain /"}
	case strings.ContainsFunc(name, unicode.IsControl):
		return &NameError{Name: name, Reason: "must not contain control characters"}
	case utf8.RuneCountInString(name) > MaxNameLength:
		return &NameError{Name: name, Reason: fmt.Sprintf("must be at most %d characters", MaxNameLength)}
	}
	return nil
}

// NameError reports a name that ValidateName refused, with the reason. It
// matches ErrInvalidName under errors.Is.
type NameError struct {
	Name   string
	Reason string
}

func (e *NameError) Error() string {
	return fmt.Sprintf("blobfs: invalid name %q: %s", e.Name, e.Reason)
}

// Unwrap returns ErrInvalidName, so errors.Is matches the sentinel.
func (e *NameError) Unwrap() error {
	return ErrInvalidName
}
