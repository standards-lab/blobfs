// Package blobfs is the root layer of the blobfs library: a SQL-backed
// virtual directory and file-metadata model over an object store the
// library never calls.
//
// The package is Go only. It holds the entity types the persistence layer
// scans into, the id of the one root directory (RootID), the status
// vocabulary and the table of allowed status transitions, key construction
// and filename sanitizing, name normalization and validation, the
// key-validation interface an object store satisfies, and the error types
// the persistence layer maps database violations onto.
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
// its display name at upload. The key is opaque. Nothing in this package or
// the layers above it parses a key, and a later rename of the row's display
// name leaves the key unchanged.
package blobfs
