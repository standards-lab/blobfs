# Glossary

The library's vocabulary, grouped by the layer each term belongs to. Terms blobfs takes from
`sqlate` are marked as such; the [sqlate
glossary](https://github.com/standards-lab/sqlate/blob/main/docs/glossary.md) defines them in
full.

## The tree

- **Install**: one tree in one database, with its objects in one container. A service that
  serves several isolated trees runs one install per tree.
- **Entity**: one of the two row types, `blobfs.Directory` and `blobfs.File`, whose `json` tags
  are the scan and binding contract of their tables.
- **Directory**: a row of `blobfs_directory`: a node of the tree with a parent and a name.
- **File**: a row of `blobfs_file`: a file's metadata, the directory it sits in, its status, and
  its key. The bytes are the consumer's, in its object store.
- **Root**: the one directory with no parent, named `/`, with the id `blobfs.RootID`, the nil
  UUID, seeded by the schema.
- **Name space**: the names one parent holds for one kind of row. Directories and files have
  separate name spaces, so a directory and a file may share a name under one parent.
- **Name**: a directory's or file's display name, normalized to Unicode NFC and validated before
  every insert and rename: non-empty, at most 255 runes, no slash, no control character, neither
  `.` nor `..`.
- **Path**: directory names separated by slashes. A path the library resolves is relative, `a/b`
  below a directory held by id; the path it computes for display starts at the root, `/a/b`.
- **Key**: the object's key in the store, `<id>/<sanitized name>`, built once when the file's
  first row is inserted, validated against the store, and never changed or parsed. It is opaque:
  it says nothing about where the file sits.
- **Sanitized name**: the key's name segment, the display name at upload with the characters an
  object store refuses replaced, frozen so an operator browsing the container can read it.
- **Key validator**: the consumer's adapter over its store's key rule, `blobfs.KeyValidator`:
  the one thing blobfs asks of an object store.

## The protocols

- **Status**: a file row's place in the write and delete protocols: `pending`, `available`, or
  `deleting`.
- **Pending**: a row inserted before its object exists. A stopped write leaves its row pending,
  where a retry resumes it.
- **Available**: a row whose object exists, with the size, content type, and entity tag the
  store reported.
- **Deleting**: a row whose object is being removed. It keeps its name until it is purged, and
  every mutation but the delete steps refuses it.
- **Transition**: a change of status the table in the root package allows. No transition leaves
  `deleting` except the row's removal.
- **Two-phase write**: `Create` (or `Ensure`) inserts the pending row, the consumer puts the
  object under its key, and `Complete` makes the row available.
- **Two-phase delete**: `Delete` marks the row deleting and returns its key, the consumer
  deletes the object, and `Purge` removes the row. Every step is safe to repeat.
- **Write outcome**: what `Files.Ensure` did with a name: created a pending row, resumed a
  pending row an earlier write left, or found the name present.
- **Hold**: `Files.Hold`, a lock on a file's row for the rest of a transaction, taken without
  changing the row. It is the library's half of reference-then-delete.
- **Reference-then-delete**: the rule that a consumer holds a file in the transaction that
  inserts a reference to it, so the reference and a delete of the file serialize on the file's
  row.
- **Tree lock**: the lock a directory move takes before its cycle check, so two opposing moves
  run one after the other. The baseline has none; the PostgreSQL engine's is an advisory lock
  held until the transaction ends.
- **Cycle check**: `Directories.IsWithin`, run by a directory move, which refuses a new parent
  inside the moved directory's own subtree.
- **Serializes**: whether a variant's tree lock serializes directory moves across transactions.

## Persistence

- **Store**: `data.Store`, blobfs's statements compiled once against the consumer's catalog,
  with the two operation handles `Directories` and `Files`.
- **Catalog** (sqlate): the set of pattern sources a program's statements compile against. A
  consumer builds one, with the query library's patterns and blobfs's.
- **Published patterns**: the column lists of the two entities, `blobfs.directory_columns` and
  `blobfs.file_columns`, which a consumer's own statements include.
- **Listing**: one page of one directory's child directories or files, a sqlate projection
  anchored on the directory's id, with the caller's filters and sort composed onto it.
- **Directives** (sqlate): a listing request's sorts, filters, and whether it counts the
  total.
- **Cursor** (sqlate): an opaque position in a listing's order that `Continue` reads the next
  page from. Only a sort in one direction over fields that are never null issues one.
- **Counted total**: the total a listing reports, a window count in the page's own statement, so
  it never disagrees with the page. An empty page after the first carries none.
- **Returning command** (sqlate): a standard-tier command that names the read of its changed
  row: a create, a complete, a move, or a delete's first step.
- **Single-statement form** (sqlate): a returning command run as one statement with
  `RETURNING`, on an engine whose dialect renders the clause.
- **Fallback** (sqlate): a returning command run as the command and then its read, in one
  transaction, on an engine whose dialect does not render `RETURNING`.
- **Guarded step**: an update that runs only at the version the caller read, and reports
  `query.ErrVersionMismatch` otherwise.
- **Violation**: a database constraint violation mapped to a blobfs sentinel, reported as a
  `blobfs.ViolationError` that names the sentinel and the constraint.

## Engines

- **Tier** (sqlate): the portability a statement declares. A standard statement runs on any
  engine; a native one uses a feature of one engine and carries a port note.
- **Baseline**: `data.Standard`, the standard-tier variant every store runs unless an engine
  replaces it, complete on any engine `sqlate` has a dialect for.
- **Variation point**: an operation an engine can do better than standard SQL: the tree lock and
  path resolution.
- **Variant**: an implementation of the variation points, `data.Variant`, that the store
  forwards them to.
- **Engine**: a `data.Engine`, the function that builds a variant over the baseline `data.New`
  compiled, installed with `data.WithEngine`. An engine sub-module ships one; a consumer may
  write its own.
- **Engine sub-module**: a module named for its engine, such as `postgres`, holding the engine's
  variant, its native statements, its migration set, and its integration tier.
- **Conformance suite**: `datatest.Run`, the checks every store must pass on a live database,
  which compares a variant's rows and refusals with the baseline's.

## Schema

- **Migration set** (sqlate): a named, self-contained layer of a schema with its own history
  table. blobfs's is `blobfs`, recorded in `blobfs_schema_version`, and a consumer declares it
  below its own.
- **Public schema**: the tables, columns, constraint names, referential actions, and migration
  set, all of which change only in a major release.
- **Constraint name**: a name of the form `blobfs_<kind>_<table>_<detail>`, which a violation
  carries to the consumer.
