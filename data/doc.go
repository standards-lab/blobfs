// Package data is blobfs's persistence: New compiles its standard-tier
// statements against the consumer's catalog into a Store, whose
// Directories and Files handles run each step on a session the caller
// passes, and whose protocols (WriteFile, EnsureFile, RemoveFile,
// PurgeFile) and Sweep run the steps end to end over the consumer's
// object store. Every refusal is a sentinel of the root package or of
// the query library, named once as "data: <op>: ".
//
// docs/concepts.md explains the protocols and docs/features.md states
// every operation, refusal, and option.
package data
