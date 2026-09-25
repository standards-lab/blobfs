// Package data is blobfs's persistence package: its standard-tier SQL
// statements, the pattern namespace it publishes, and the Store, whose
// Directories and Files handles run the statements through sqlate on any
// engine sqlate has a dialect for. New compiles the statements once
// against the consumer's pattern catalog; Store.Sweep finishes the deletes
// callers began. The Variant interface names the variation points an
// engine installed through WithEngine may implement natively.
//
// Every operation takes the context and a session first and passes the
// session through unwrapped, so a call runs on the pool or inside the
// caller's transaction. An operation correct only inside a transaction
// takes a *sqlate.Tx: Directories.Move, Directories.LockTree,
// Directories.MarkDeleting, Files.Hold, and Files.Delete. Store.Sweep
// takes the *sqlate.DB, since it opens transactions of its own.
//
// Every refusal is a sentinel of the root package, or of the query
// library, matched with errors.Is; a violation of one of blobfs's
// constraints is a blobfs.ViolationError naming the sentinel and the
// constraint. blobfs.ErrDeleting outranks query.ErrVersionMismatch: a
// step that refuses a deleting row reports it whatever version the caller
// names. Each exported method names its error once, as "data: <op>: ".
//
// docs/concepts.md explains the protocols and docs/features.md states
// every operation, refusal, and option in full.
package data
