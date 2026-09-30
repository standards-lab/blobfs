// Package data is blobfs's persistence: its standard-tier SQL statements,
// the pattern namespace it publishes, and the Store that runs the
// statements through sqlate on any engine sqlate has a dialect for.
//
// The catalog and the engine:
//
//   - Namespace is the pattern namespace blobfs publishes, and Patterns its
//     pattern source, which a consumer registers in its catalog.
//   - New compiles the statements once against that catalog into a Store;
//     Option configures it, and WithEngine installs an Engine.
//   - Engine builds a Variant, the variation points an engine implements
//     natively, over the baseline it is given.
//   - Store.Statements lists the compiled inventory, and Store.Verify
//     prepares it against the schema.
//
// The handles, whose methods run one step each on the session the caller
// passes:
//
//   - Store.Directories, a *Directories, creates, finds, lists, moves, and
//     deletes directory rows and marks a branch deleting.
//   - Store.Files, a *Files, creates, completes, finds, lists, moves,
//     holds, deletes, and purges file rows. Files.Ensure reports what it
//     did with the name as a WriteOutcome: WriteCreated, WriteResumed, or
//     WritePresent.
//   - Listing is the interface both handles' listings satisfy.
//   - Marked counts the rows Directories.MarkDeleting changed.
//
// The options:
//
//   - ListOption configures a listing; IncludeDeleting lists deleting rows
//     too.
//   - CreateOption configures a create; WithID supplies the row's id.
//   - VersionOption configures a guarded step; AtVersion names the
//     version.
//   - SweepOption configures a sweep: Batch bounds a pass,
//     OnRemoveDirectory runs the consumer's cleanup in each directory
//     removal's transaction, and StaleOlderThan also reclaims the pending
//     and deleting file rows older than an age.
//
// The protocols, which run the steps end to end over the consumer's object
// store:
//
//   - ObjectStore is the store as the write protocols call it, a
//     blobfs.KeyValidator, an ObjectPutter, and an ObjectDeleter; the
//     delete protocols and the sweep take an ObjectDeleter alone.
//   - Store.WriteFile runs the two-phase write, and Store.EnsureFile its
//     retry-safe form under a fixed id.
//   - Store.RemoveFile runs the two-phase delete of the file a callback
//     picks, Store.RemoveFileID of the file with an id, and Store.PurgeFile
//     the last two steps of a delete already begun.
//   - Store.Sweep runs one bounded pass that finishes the deletes callers
//     began and reports it as a SweepResult; SweepUntilDone runs a
//     consumer's passes until the work is done.
//
// Every operation takes the context and a session first and passes the
// session through unwrapped, so a call runs on the pool or inside the
// caller's transaction. An operation correct only inside a transaction
// takes a *sqlate.Tx: Directories.Move, Directories.LockTree,
// Directories.MarkDeleting, Files.Hold, and Files.Delete. The Store's
// protocols and Sweep take the *sqlate.DB, since they open transactions of
// their own.
//
// Every refusal is a sentinel of the root package, or of the query
// library, matched with errors.Is. A violation of one of blobfs's
// constraints is a blobfs.ViolationError naming the sentinel and the
// constraint, and blobfs.ErrDeleting is a blobfs.DeletingError naming
// whose delete refused. blobfs.ErrDeleting outranks
// query.ErrVersionMismatch: a step that refuses a deleting row reports it
// whatever version the caller names. Each exported method names its error
// once, as "data: <op>: ", except the errors of the caller's own
// callbacks, begin and pick, which the protocols return as they came.
//
// docs/concepts.md explains the protocols and docs/features.md states
// every operation, refusal, and option.
package data
