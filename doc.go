// Package blobfs is the root package of blobfs, a SQL-backed tree of
// directories and file metadata over an object store the library does not
// import.
//
// Ids and names:
//
//   - NewID mints a row id, and ParseID checks one a caller supplies.
//   - RootID is the id of the one root directory of an install.
//   - NormalizeName puts a name in the form uniqueness compares, and
//     ValidateName checks it; MaxNameLength is the longest name allowed.
//
// The entities and their statuses:
//
//   - Directory is a node of the tree, File a file's metadata, and Object
//     what the store reports about a stored object.
//   - Status is a file's place in the two-phase write and delete:
//     StatusPending, StatusAvailable, or StatusDeleting. Transition checks
//     a change of Status.
//   - DirectoryStatus is a directory's: DirectoryStatusActive or
//     DirectoryStatusDeleting.
//
// Keys:
//
//   - KeyValidator is the one question a write asks of the object store.
//   - NewKey builds a file's key, <id>/<sanitized name>, and validates it;
//     SanitizeFilename builds the name's segment. A key is opaque: nothing
//     in blobfs parses it.
//
// Errors:
//
//   - The sentinels, ErrNotFound through ErrCycle, are what package data
//     maps its outcomes onto, matched with errors.Is.
//   - IDError, NameError, KeyError, and TransitionError report a refused
//     id, name, key, and status change, each matching its sentinel.
//   - ViolationError reports a violation of one of blobfs's constraints,
//     naming the sentinel and the constraint; DeletingError reports a
//     mutation refused because a row's delete has begun, naming whose.
//   - The Constraint constants, ConstraintPrimaryKeyDirectory and its
//     siblings, name the constraints and unique indexes blobfs's DDL
//     declares, as ViolationError.Constraint carries them.
//
// The package is Go only; its one dependency outside the standard library
// is golang.org/x/text, for Unicode normalization. A consumer that keeps
// its own persistence takes only this package.
//
// docs/concepts.md explains the model and docs/features.md states every
// rule, the schema included.
package blobfs
