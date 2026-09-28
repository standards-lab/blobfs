// Package blobfs is the root package of blobfs, a SQL-backed tree of
// directories and file metadata over an object store the library does not
// import: the entity types, the root's id (RootID), the status vocabulary
// and its transitions, key construction and filename sanitizing, name
// normalization and validation, the KeyValidator interface a store
// satisfies, and the errors the persistence package maps violations onto.
//
// The package is Go only; its one dependency outside the standard library
// is golang.org/x/text, for Unicode normalization. A consumer that keeps
// its own persistence takes only this package. A file's key,
// <id>/<sanitized name>, is opaque: nothing in blobfs parses it.
//
// docs/concepts.md explains the model and docs/features.md states every
// rule, the schema included.
package blobfs
