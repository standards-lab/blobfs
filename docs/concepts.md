# Concepts

blobfs keeps a tree of directories and file rows in SQL over objects a consumer stores in an
object store under opaque keys. This document explains the model: the tree, the keys, the
protocols that keep rows and objects consistent, and the layers a consumer builds on. The
[glossary](glossary.md) defines the terms; the [features](features.md) document states every
rule.

## A virtual tree over opaque keys

An object store is flat: a container of objects, each under a key. A browsable tree needs
parents, names, and moves, and an object store has no atomic rename, so a key that encodes where
a file sits turns a move of a directory into a copy and a delete of everything beneath it.
blobfs keeps the tree in SQL instead and leaves the keys flat.

A file's key is its id, a slash, and a sanitized copy of its name at upload:

```
0199a5d2-4c7e-7c1a-9f3e-5b2d8a6c4e10/Q3 summary.txt
```

The key is built once, when the file's first row is inserted, and never changes. A rename or a
move of the file is an update of its row, and a move of a directory is an update of one row; no
object moves, because no key says where its file sits. Nothing in blobfs parses a key, and a
consumer's authorization never should: the row, not the key, says what a file is and where it
sits. The name segment exists so an operator browsing the raw container can read it; it is
frozen at upload and goes stale after a rename by design.

blobfs never calls the object store. It exposes the steps of each protocol that involves an
object, and the consumer runs its own put or delete between them. The one thing blobfs asks of
the store is whether it accepts a key, through a one-method interface the consumer wires to its
store's own rule.

## Directories and files

Directories and files are separate resources: two tables, `blobfs_directory` and `blobfs_file`,
with two operation handles, `Directories` and `Files`. They have separate name spaces, so a
directory and a file may share a name under one parent. A name is unique within its parent for
its own kind, compared exactly after Unicode normalization to NFC, so a composed and a
decomposed spelling of one name collide.

Every operation takes its row by id. A path is an entry point: `FindByPath` resolves a relative
path such as `reports/2026` below a directory the caller holds by id, and `Path` computes a
directory's path from the root at read time. The library has no absolute path; a consumer whose
own input syntax writes `/reports/2026` strips the leading slash and resolves from the root.

## The root

An install has one root directory, seeded by the schema: the row with no parent, named `/`,
whose id is `blobfs.RootID`, the nil UUID. Every consumer addresses the root by that id instead
of searching for it. A partial unique index allows no second row without a parent, and a check
constraint ties the name `/` to the absence of a parent.

The root is in no listing: the listing of `blobfs.RootID` is the depth-one directories. It
cannot be deleted, moved, or renamed. Files may sit in it.

## The two-phase write

A file is written in two steps around the object put, and a row exists before any byte does:

1. `Files.Create` inserts the row as `pending` and returns it. The row's `Key` is where the
   consumer stores the object.
2. The consumer puts the object under `Key`.
3. `Files.Complete` records what the store reported, the size, the content type, and the entity
   tag, and moves the row to `available`. It is guarded by the version the pending row carried.

Both steps take any session. The first can run in the consumer's own transaction beside the
consumer's own rows, so the pending row and the consumer's record of it commit together before
the put. The last runs on the pool or in a transaction.

There is no failed status. A write that stops after the first step leaves the row `pending`,
where `Files.FindByName` or a listing filtered on status finds it. `Files.Ensure` is the
retry-safe first step: it reports whether it created the row, resumed a pending row an earlier
attempt left, or found the name held by a row that is available or deleting. A resumed row
carries its original key, so a retried put overwrites the same object. A write that is abandoned
is removed through the delete steps, which a pending row accepts.

## The two-phase delete

A file is deleted in two steps around the object delete:

1. `Files.Delete` moves the row to `deleting` and returns it with its key. It takes a
   transaction (see [the hold](#reference-then-delete)).
2. The consumer deletes the object under `Key`. An object store treats a missing key as success,
   so the step is safe to repeat.
3. `Files.Purge` removes the row.

Every step is idempotent. `Delete` returns a row that is already deleting as it is, without
advancing its version again. `Purge` succeeds when the row is already gone, because a retry
after a crash cannot tell its own earlier success from a row that never existed, and it refuses
a row that is not deleting with `blobfs.ErrNotDeleting`, since that row's object may still be
wanted. A stop at any point is resumed by running the steps again from the first.

A deleting row keeps its name until it is purged, so a write of the same name in the window is
refused as taken. Every mutation other than the delete steps refuses a deleting row with
`blobfs.ErrDeleting`, so no operation acts on a row whose object is gone or about to be.

## Statuses and their transitions

A file row's status is one of three, and the root package holds the table of allowed changes:

| From | To | Step |
|---|---|---|
| `pending` | `available` | `Complete` |
| `pending` | `deleting` | `Delete`, of an abandoned write |
| `available` | `deleting` | `Delete` |
| `deleting` | `deleting` | `Delete`, retried |

No change leaves `deleting` except the row's removal. A refused change is a
`blobfs.TransitionError`.

## Who calls each step

| Step | Caller | Session |
|---|---|---|
| `Files.Create` or `Files.Ensure` | the consumer, often beside its own rows | the pool or a transaction |
| the put | the consumer, through its object store | none |
| `Files.Complete` | the consumer | the pool or a transaction |
| `Files.Delete` | the consumer, after its own reference check | a transaction |
| the object delete | the consumer, through its object store | none |
| `Files.Purge` | the consumer | the pool or a transaction |

## Reference-then-delete

A consumer's own row may reference a file, through a foreign key of its own into `blobfs_file`.
Two transactions can race: one inserts a reference while another begins the file's delete.
`Files.Hold` closes the race. A consumer holds the file's row in the transaction that inserts
its reference, before the insert. The hold takes the row's lock without changing its version,
and `Files.Delete`, whose update takes the same lock, waits for the holding transaction to end.
Once `Delete` returns, every reference a hold admitted has committed, so the consumer checks for
its own references in the same transaction, after the call. A file whose delete has begun cannot
be held.

The consumer's own foreign key is the backstop: `Purge` of a row a consumer's row still
references is refused with `blobfs.ErrReferenced`, the row stays deleting, and a retry after the
reference is gone converges.

## Moves

A move of a directory or a file is an update of its parent and its name; a rename is a move to
the same parent. A file move needs nothing more, because a file cannot be its own ancestor.

A directory move could make a directory its own ancestor, so `Directories.Move` runs three
statements in the caller's transaction: it takes the tree lock, checks that the new parent is
not inside the directory's own subtree (`Directories.IsWithin`), and runs the update, guarded by
the version the caller read. The lock is what makes the check sound under concurrency: two
opposing moves, A under B and B under A, each pass their check against the same committed tree
unless one waits for the other.

Standard SQL has no statement that holds a lock until commit, so the baseline's tree lock is a
no-op, and `Directories.Serializes` reports `false`. On the baseline, two concurrent opposing
moves can both commit and leave two directories each other's ancestor, detached from the root. A
consumer on the baseline takes one of three courses:

- runs every transaction that moves a directory at serializable isolation,
  `sqlate.Isolation(sql.LevelSerializable)`, and retries on `sqlate.ErrSerializationFailure`,
  which the engine returns for the second of two opposing moves;
- serializes directory moves outside the database;
- installs an engine whose variant takes a real lock, such as the PostgreSQL engine's advisory
  lock.

A caller that needs the guarantee checks `Serializes` before its first move.

## Listings

A listing reads one directory's contents, one page at a time: `Directories.List` the child
directories, `Files.List` the files. No operation lists across directories or walks the tree.

Each listing is a projection of sqlate's `query` package. Its base is an authored statement
anchored on the directory, and the `query` package composes the caller's `query.Directives`
onto it: the filters, the sort, and whether to count. Only the fields the base declares may be
filtered or sorted, and anything else is refused before any SQL. The key is `name`, unique
within the directory, so the default order is by name and every sort is total.

A page is read by number with `List`, or past a cursor with `Continue`. A page carries a cursor
only when more rows remain and its sort can be continued: the sort runs in one direction and
names no nullable field. A cursor is opaque and is refused if edited or presented to another
listing, another sort, or other filters.

The total is counted in the page's own statement, a window count over the filtered rows, so it
never disagrees with the page. An empty first page reports 0. An empty page after the first, and
an empty continued page, carry no count and report `query.NoTotal`, since no row carries it and
a second statement could contradict the page. The count reads every filtered row before it
pages, so a caller walking a large directory by cursor reads the total once and passes
`query.TotalNone` after, which lets the read stop early along the index.

## Ownership stays with the consumer

blobfs has no owner, no unit, and no authorization anywhere in its schema or its API. A consumer
composes ownership through its own tables, at the grain its domain needs:

- at the directory grain, a row that binds a top-level directory to a unit, checked before the
  consumer resolves anything below it;
- at the file grain, a join row that binds one file to a unit, with the consumer's own
  constraints, such as a partial unique index for one active file per unit.

The consumer's check runs around the library's listing, never inside it. For its own read
models, blobfs publishes the column lists of its two entities as patterns,
`blobfs.directory_columns` and `blobfs.file_columns`, which a consumer's statements include so
they scan into `blobfs.Directory` and `blobfs.File`.

## Engines and variants

Every statement the persistence package ships is standard SQL, and the package is complete
alone: any engine `sqlate` has a dialect for runs every operation through it. Two operations are
variation points, where an engine can do better than standard SQL: the tree lock, and path
resolution, which the baseline walks one segment per statement. The `data.Variant` interface
names them, with `Serializes`; `data.Standard` is the baseline.

An engine sub-module adds an engine's native forms and its DDL. It ships a `data.Engine`, which
`data.New` calls with the baseline it compiled, and a consumer installs it with
`data.WithEngine`. The PostgreSQL sub-module's variant takes a transaction-scoped advisory lock
and resolves a path of any depth in one statement. Each native statement declares its tier and a
port note, the engine feature it uses and what a port to another engine must provide.

The other native forms need no variant. A command that returns its changed row, a create, a
complete, a move, or a delete's first step, is a returning command of `sqlate`, and the dialect
chooses its form when the statement compiles: the single-statement form, with `RETURNING`, on an
engine that has the clause, and elsewhere the fallback, the command and a read of the row in one
transaction. The cursor's
keyset predicate is a pattern an engine's dialect module overlays, so a consumer on PostgreSQL
registers `sqlate/postgres`'s patterns in its catalog in place of `query.Patterns()`.

A consumer can write an engine of its own: a `data.Engine` whose variant embeds the baseline, or
an engine's variant, and overrides the methods it needs. The conformance suite, `data/datatest`,
checks any variant against the baseline's outcomes on a live database.

## One install per configuration

An install is one tree in one database, with objects in one container. blobfs's object names are
fixed, and nothing in the library names a second database, schema, or container: there is no
volume id and no key prefix. A service that serves several isolated trees runs one configuration
per tree, each with its own database and its own container, so an operation on a container
belongs to exactly one tree.
