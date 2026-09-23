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
// transaction; an operation correct only inside a transaction, the move
// and the tree lock, takes a *sqlate.Tx. A violation of one of blobfs's
// own constraints becomes blobfs.ErrNameTaken, blobfs.ErrIDTaken,
// blobfs.ErrNotFound, blobfs.ErrRootDirectory, or, on a delete,
// blobfs.ErrNotEmpty, carried by a blobfs.ViolationError. A violation of a
// constraint blobfs does not own returns unclassified on a write, wrapped
// with the operation's context; on a delete a foreign key blobfs does not
// own is a consumer's row that references the one being removed, which the
// package reports as blobfs.ErrReferenced by the violation's class, with
// the constraint's name reachable for the consumer to match.
package data
