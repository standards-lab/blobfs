// Package blobfs is the root package of blobfs: the model of a SQL-backed
// tree of directories and file metadata over an object store the library
// never calls.
//
// The package is Go only. It holds the entity types package data scans
// into, the id of the one root directory (RootID), the status vocabulary
// and the table of allowed status transitions, key construction and
// filename sanitizing, name normalization and validation, the
// key-validation interface an object store satisfies, and the error types
// package data maps database violations onto.
//
// An install is one directory tree in one database: the schema seeds a
// single root directory, named / and with the well-known id RootID, and a
// partial unique index allows no second root. A consumer that wants several
// isolated trees runs several installs, each with its own database and its
// own container.
//
// It imports neither sqlate nor go-storage; its one dependency outside the
// standard library is golang.org/x/text/unicode/norm, because the standard
// library has no Unicode normalizer.
//
// A consumer that keeps its own persistence takes only this package: it
// authors its own DDL from the documented schema, mints ids with NewID,
// normalizes and validates names before every insert and rename, builds
// each file's key with NewKey, and checks status changes against the
// transition table. The persistence package, data, and each engine
// sub-module import this one.
//
// A file's key is [id]/[filename]: the file's id and a sanitized copy of
// its display name at upload. The key is opaque. Nothing in blobfs parses
// a key, and a rename of the row leaves the key unchanged.
package blobfs
