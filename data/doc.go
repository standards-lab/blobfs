// Package data is the persistence layer of blobfs over the root package's
// entities. It holds blobfs's own SQL statements, the pattern namespace it
// publishes, and the Store, whose operation handles run the statements
// against a sqlate.Session the caller provides. It is the standard-tier
// baseline: every statement and every pattern is standard SQL, so the
// package runs on any engine sqlate has a dialect for. Ids are minted in
// Go with blobfs.NewID, or supplied by the caller through WithID so a
// seeded row keeps its id across resets. A command that needs its row back
// declares the read that returns it, and the dialect chooses the form: one
// statement on an engine that renders RETURNING, the command and the read
// in one transaction elsewhere.
//
// A consumer builds one pattern catalog for its whole program, with
// query.Patterns() and Patterns() registered beside its own sources, and
// passes it to New, which compiles the embedded statements against it. The
// published patterns are the column lists the entity types scan,
// blobfs.directory_columns and blobfs.file_columns, which a consumer's own
// statements include with {{> blobfs.name}}. Every pattern is
// parameter-free, so a projection base that includes one binds no
// parameters of its own.
//
// The tree has one root, seeded by the schema with blobfs.RootID. The
// Store's Directories handle reads and writes directories. Ids are the
// primary handle: every operation takes a directory by id, and a path is
// an entry point. Find reads a directory by id and FindByName a child by
// name; FindByPath resolves a relative path below a directory the caller
// holds by id, and Path computes a directory's path from the root at read
// time. Create inserts a directory and Ensure is the insert-or-find a
// seeder runs, which looks the name up first and inserts only when it
// found no row. Move moves or renames a directory inside a transaction,
// under the variant's tree lock and a cycle check, guarded by the version
// the caller read. Delete removes one empty directory; there is no cascade
// and no recursive delete, and a consumer that wants one walks the tree,
// files and then directories, deepest first. No operation walks the whole
// tree.
//
// The Store's Files handle reads and writes file rows, and never calls the
// object store: the consumer sequences each protocol around its own put
// and delete. A write is Create, which inserts the row as pending with the
// key built by blobfs.NewKey and validated against the consumer's
// blobfs.KeyValidator, then the object put under the row's Key, then
// Complete, which records what the store reported and makes the row
// available, guarded by the pending row's version. Ensure is the
// retry-safe first step: it reports whether it created the row, resumed a
// pending one an earlier write left, or found the name present. There is
// no failed status: a stop leaves the row pending for a retry, and an
// abandoned write is deleted like any file. A delete is Delete, which
// marks the row deleting and returns it with its key, idempotent on a row
// already deleting; then the object delete; then Purge, which removes the
// row, succeeds on a row already gone, and refuses one that is not
// deleting. Every other mutation refuses a deleting row, and a deleting
// row keeps its name until it is purged. Hold is the library's half of the
// reference-then-delete rule: a consumer holds a file's row in the
// transaction that inserts a reference to it, and Delete, which takes the
// same row lock, waits for that transaction. Find and FindByName read a
// file whatever its status, and Move moves or renames one, guarded by
// version, without touching its key.
//
// Two operations are variation points, where an engine may do better than
// standard SQL: the tree lock that serializes directory moves, and path
// resolution. The Variant interface names them; Standard is the baseline
// every engine runs (a no-op lock that reports it does not serialize, and
// one child read per path segment), and New takes another implementation
// through WithVariant: an engine sub-module's, or a consumer's own, which
// embeds either and overrides the methods it needs. The Store validates
// every input and classifies every error itself, so a variant binds what
// it is given and returns what the session mapped.
//
// Every operation takes the session as an argument and passes it through
// unwrapped, so a call runs against the pool or inside the caller's
// transaction; an operation correct only inside a transaction, the
// directory move, the tree lock, a file's hold, and a file's Delete, takes
// a *sqlate.Tx. A guarded step whose row moved on is
// query.ErrVersionMismatch, with the expected and current versions in the
// text; a step a deleting row refuses is blobfs.ErrDeleting, and a status
// change the transition table refuses a blobfs.TransitionError. A
// violation of one of blobfs's own constraints becomes
// blobfs.ErrNameTaken, blobfs.ErrIDTaken, blobfs.ErrNotFound,
// blobfs.ErrRootDirectory, or, on a directory delete, blobfs.ErrNotEmpty,
// carried by a blobfs.ViolationError. A violation of a
// constraint blobfs does not own returns unclassified on a write, wrapped
// with the operation's context; on a delete a foreign key blobfs does not
// own is a consumer's row that references the one being removed, which the
// package reports as blobfs.ErrReferenced by the violation's class, with
// the constraint's name reachable for the consumer to match.
package data
