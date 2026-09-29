// Package data is blobfs's persistence package: its standard-tier SQL
// statements, the pattern namespace it publishes, and the Store, whose
// Directories and Files handles run the statements through sqlate on any
// engine sqlate has a dialect for. New compiles the statements once
// against the consumer's pattern catalog. Store.Write, Store.Ensure,
// Store.Remove, and Store.Purge run the two-phase write and delete over
// the consumer's object store; Store.Sweep and Store.SweepUntilDone finish
// the deletes callers began. The Variant interface names the variation
// points an engine installed through WithEngine may implement natively.
//
// Every operation takes the context and a session first and passes the
// session through unwrapped, so a call runs on the pool or inside the
// caller's transaction. An operation correct only inside a transaction
// takes a *sqlate.Tx: Directories.Move, Directories.LockTree,
// Directories.MarkDeleting, Files.Hold, and Files.Delete. The Store's own
// methods take the *sqlate.DB, since they open transactions of their own.
//
// Every refusal is a sentinel of the root package, or of the query
// library, matched with errors.Is; a violation of one of blobfs's
// constraints is a blobfs.ViolationError naming the sentinel and the
// constraint, and blobfs.ErrDeleting is a blobfs.DeletingError naming
// whose delete refused. blobfs.ErrDeleting outranks
// query.ErrVersionMismatch: a step that refuses a deleting row reports it
// whatever version the caller names. Each exported method names its error
// once, as "data: <op>: ", except the errors of the caller's own
// callbacks, which the Store's protocols return as they came.
//
// docs/concepts.md explains the protocols and docs/features.md states
// every operation, refusal, and option in full.
package data
