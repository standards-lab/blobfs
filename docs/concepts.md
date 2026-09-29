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

blobfs imports no object store. It exposes the steps of each protocol that involves an object,
and the consumer runs its own put or delete between them. Every write asks the store whether it
accepts a key, through a one-method interface the consumer wires to its store's own rule. The
one other call is the sweep's, which deletes objects through a second one-method interface when
the consumer runs it (see [deleting a branch](#deleting-a-branch)).

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
is removed through the delete steps, which a pending row accepts, or by a sweep that reclaims
[stale rows](#stale-rows-and-orphaned-objects).

`data.Store.Write` runs the three steps end to end, with the put through the consumer's
`data.ObjectPutter`: the first step in a transaction around the consumer's callback, the put
outside any transaction, the completion on the pool, and the abandon when the put or the
completion fails. `data.Store.Ensure` is its retry-safe form for a file under a fixed id. A
consumer calls the steps itself only when its write does not fit that shape; see [the
protocols](features.md#the-protocols).

## The two-phase delete

A file is deleted in two steps around the object delete:

1. `Files.Delete` moves the row to `deleting` and returns it with its key. It takes a
   transaction (see [the hold](#reference-then-delete)).
2. The consumer deletes the object under `Key`. An object store treats a missing key as success,
   so the step is safe to repeat.
3. `Files.Purge` removes the row.

Every step is idempotent. `Delete` returns a row that is already deleting as it is, without
advancing its version again. `Delete` with `data.AtVersion` moves the row only at the version
the caller read, for a caller that acts on a listing without reading the row again. `Purge`
succeeds when the row is already gone, because a retry after a crash cannot tell its own earlier
success from a row that never existed; a caller that wants to report a missing file resolves it
before `Delete`. `Purge` refuses a row that is not deleting with `blobfs.ErrNotDeleting`, since
that row's object may still be wanted. A stop at any point is resumed by running the steps again
from the first. A deleting row is hidden from the listings, so a delete that stops and is never
resumed leaves a row only a read by id or name, a listing with `data.IncludeDeleting`, or a
sweep finds.

`data.Store.Remove` runs the three steps end to end, the first in a transaction around the
consumer's callback, where it removes its own references, and `data.Store.Purge` runs the last
two for a consumer that began the delete in a transaction of its own; see [the
protocols](features.md#the-protocols).

A deleting row keeps its name until it is purged, so a write of the same name in the window is
refused as taken. Every mutation other than the delete steps refuses a deleting row with
`blobfs.ErrDeleting`, so no operation acts on a row whose object is gone or about to be. The
refusal is a `blobfs.DeletingError`, which tells the file's own delete from a directory's, so a
consumer can report "the file is being deleted" apart from "the folder is being deleted" (see
[errors and constraints](features.md#errors-and-constraints)).

### Deleting outranks the version

A step that refuses a deleting row, or a row in a deleting directory, reports
`blobfs.ErrDeleting` whatever version the caller names, not `query.ErrVersionMismatch`.
`Files.Delete` and a [mark](#deleting-a-branch) advance the version of each row they change, so
a writer or a mover that read the row before the delete began holds a version the row no longer
carries, and a reread and retry could never resolve the mismatch; the refusal tells it why. The
same holds for the delete steps themselves: a `Files.Delete` or a mark with `data.AtVersion`
that finds its row already deleting is the step's retry, which converges whatever the version.

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

A directory row has a status of its own, `blobfs.DirectoryStatus`, with two values: `active`,
which every directory is from its insert, and `deleting`, which only [the delete of a
branch](#deleting-a-branch) sets and nothing undoes.

## Who calls each step

| Step | Caller | Session |
|---|---|---|
| `Files.Create` or `Files.Ensure` | the consumer, often beside its own rows, or in `Store.Write`'s callback | the pool or a transaction |
| the put | the consumer through its object store, or `Store.Write` through the consumer's `ObjectPutter` | none |
| `Files.Complete` | the consumer, or `Store.Write` | the pool or a transaction |
| `Files.Delete` | the consumer, after its own reference check, or `Store.Remove` after its callback | a transaction |
| the object delete | the consumer through its object store, or `Store.Remove` and `Store.Purge` through its `ObjectDeleter` | none |
| `Files.Purge` | the consumer, or `Store.Remove` and `Store.Purge` | the pool or a transaction |
| `Directories.MarkDeleting` | the consumer, to delete a branch | a transaction |
| `Store.Sweep`, alone or in `SweepUntilDone`'s passes | the consumer, now and then, with its object delete | the pool |

## Reference-then-delete

A consumer's own row may reference a file, through a foreign key of its own into `blobfs_file`.
Two transactions can race: one inserts a reference while another begins the file's delete.
`Files.Hold` closes the race. A consumer holds the file's row in the transaction that inserts
its reference, before the insert. The hold takes the row's lock without changing its version,
and `Files.Delete`, whose update takes the same lock, waits for the holding transaction to end.
Once `Delete` returns, every reference a hold admitted has committed, so the consumer checks for
its own references in the same transaction, after the call. A file whose delete has begun cannot
be held. The mark of a branch takes each of its files' row locks the same way, so it too waits
on a hold.

The consumer's own foreign key is the backstop: `Purge` of a row a consumer's row still
references is refused with `blobfs.ErrReferenced`, the row stays deleting, and a retry after the
reference is gone converges.

## Moves

A move of a directory or a file is an update of its parent and its name; a rename is a move to
the same parent. A file move needs nothing more, because a file cannot be its own ancestor. A
move of anything out of a deleting directory, or into one, is refused with `blobfs.ErrDeleting`,
so nothing leaves or enters a branch being deleted.

A directory move could make a directory its own ancestor, so `Directories.Move` runs three
statements in the caller's transaction: it takes the tree lock, checks that the new parent is
not inside the directory's own subtree (`Directories.IsWithin`), and runs the update, guarded by
the version the caller read. The lock is what makes the check sound under concurrency: two
opposing moves, A under B and B under A, each pass their check against the same committed tree
unless one waits for the other. It serializes only the transactions that take it, so every
change to the tree's shape takes it: a move does, and a consumer takes it for a change of its
own through `Directories.LockTree`.

Standard SQL has no statement that holds a lock until commit, so the baseline's tree lock is a
no-op, and `Directories.Serializes` reports `false`. On the baseline, two concurrent opposing
moves can both commit and leave two directories each other's ancestor, detached from the root.
A [mark](#deleting-a-branch) races a move the same way: its walk reads the branch as committed
when its statement starts, so a directory moved out of the branch by a move that commits while
the mark waits on the directory's row is still marked, deleting under an active parent, and
swept. A consumer on the baseline takes one of three courses, for its moves and its marks alike:

- runs every transaction that moves a directory or marks a branch at serializable isolation,
  `sqlate.Isolation(sql.LevelSerializable)`, and retries on `sqlate.ErrSerializationFailure`,
  which the engine returns for the second of two conflicting transactions;
- serializes directory moves and marks outside the database;
- installs an engine whose variant takes a real lock, such as the PostgreSQL engine's advisory
  lock.

A caller that needs the guarantee checks `Serializes` before its first move or mark.

The lock's guarantee assumes read committed isolation, the default, where each statement reads
the tree as committed when it starts: the second mover's check runs after its lock returns, and
so sees the first mover's commit. At repeatable read or serializable, a transaction reads the
tree as of its first statement, and on PostgreSQL that is the lock statement itself, whose
snapshot is taken before it blocks; the second mover's check then does not see the first move
and passes. The engine refuses the second move instead, at its update or commit, with
`sqlate.ErrSerializationFailure`: at serializable on any engine that implements it, and at
repeatable read on PostgreSQL because the update's foreign-key check locks the new parent, which
the first move changed. The caller retries a refused move in a new transaction. Either way no
cycle forms on PostgreSQL, but the refusal is the engine's, not the lock's.

If a cycle does form, on the baseline without any of the courses above, the upward walks still
terminate: `IsWithin` answers true for any directory on the loop and false for any off it, the
root among them, and `Path` reports `blobfs.ErrCycle` for a directory on the loop or below it. A
`Move` of a directory on the loop back under the root passes the cycle check and repairs the
tree.

## Listings

A listing reads one directory's contents, one page at a time: `Directories.List` the child
directories, `Files.List` the files. No operation lists across directories or walks the tree.

A listing hides deleting rows: the directories of a branch being deleted, and the files whose
delete began or whose branch was marked. It appends a filter on status after the caller's own,
so neither the page nor the total holds them, and the listing of a directory that is itself
deleting is refused with `blobfs.ErrDeleting`. A directory that does not exist still lists
empty. `data.IncludeDeleting` shows deleting rows and lists a deleting directory: it is how a
sweep, or a consumer's own tool, finds the work of a delete.

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

## Deleting a branch

`Directories.Delete` removes one empty directory. It needs no version guard: the one change a
stale version would catch, a child added since the caller read the directory, the foreign keys
refuse already; `data.AtVersion` adds one for a caller that acts on the version its user saw. A
branch, a directory with everything beneath it, is deleted in two stages instead, because its
objects live in a store no transaction reaches and there may be more of them than one pass
should hold:

1. `Directories.MarkDeleting` marks the branch in one transaction: the directory it names, every
   directory beneath it, and every file in them move to `deleting`, each row's version advancing
   once, and it reports how many of each it changed.
2. `Store.Sweep` removes the branch: it deletes each file's object through the consumer's
   `data.ObjectDeleter` and purges the file's row, as a file delete's last two steps do, and
   removes each directory once it is empty, a directory's children before the directory and the
   branch's root last.

From the mark's commit the branch is hidden and closed. The listings hide its rows, and the
listing of a directory in it is `blobfs.ErrDeleting`. A create or an ensure under a deleting
directory, a move into one, and a move of a directory or file out of one are
`blobfs.ErrDeleting`: nothing enters the branch and nothing leaves it. A read by id, name, or
path still finds its rows, with their status. A mark is never undone, and the root cannot be
marked.

The mark is guarded like the other steps a caller takes on a row it read. `data.AtVersion` marks
the branch only while its root is at the version the caller read, the directory its user saw and
confirmed; a directory at another version is `query.ErrVersionMismatch`, and nothing is marked.
A directory already deleting is the mark's retry and reports what it reached anew (see
[deleting outranks the version](#deleting-outranks-the-version)).

### Stragglers and convergence

The mark runs under the tree lock. Where the lock serializes, no move reshapes the branch while
the mark walks it; on the baseline, a consumer takes one of the courses [moves](#moves) gives.
No lock stops a create that read its parent as active before the mark committed: that row, a
straggler, lands in the branch active. A repeated mark reaches it, since the mark walks through
rows already deleting. The sweep walks each branch without marking it, and marks it again only
when the walk meets a straggler, an active row or a directory refused as not empty, since a
mark walks the whole branch and on the PostgreSQL engine holds the tree lock while it does. It
marks a branch again at most once a pass; a straggler that lands after that mark stops the
branch's walk, and the pass reports `More`; the next pass marks it and goes on.

`Files.Ensure` and `Directories.Ensure` return an active row they find by name, even one that
landed in a deleting directory as a straggler, without reading the directory: the read would
narrow the race and not close it, since a mark can commit just after it. The straggler's fate is
the one any create that raced the mark meets: the sweep marks it and removes it, and a write's
`Complete` of it is refused once it is marked (see
[orphaned objects](#stale-rows-and-orphaned-objects)).

The sweep keeps no state. On every pass it finds its work in the database:
`Directories.Deleting` returns the roots of the branches being deleted, each a deleting
directory under an active parent, and the listings with `data.IncludeDeleting` reach the rest of
each branch. Every step it takes is idempotent, so a pass stopped at any point, by an error or a
crash, is finished by the next, and a row another pass removed first counts as done. A pass is
bounded by `data.Batch`, a count of records and not of bytes, since a pass never reads an
object's size, and reports `More` when work remains that it did not reach; a consumer runs
passes while `More` is true, and again on a schedule of its own. A refusal leaves the row it
meets, and the directories above it, which cannot be removed while it remains, for a later pass,
and the pass goes on past it: to the row's siblings, to the next branch, and to the next stale
row, each read past the ones the pass left, so a row refused on every pass holds back nothing
behind it. A refusal spends none of the budget and is not `More`: the pass returns every refusal
joined, with a result that counts what it did, and a later pass tries the row again.

### The consumer's own rows

A consumer's row that references a directory in the branch, such as the owner row that binds a
top-level directory to a unit (see [ownership](#ownership-stays-with-the-consumer)), would
refuse the directory's removal through its foreign key as `blobfs.ErrReferenced`.
`data.OnRemoveDirectory` runs the consumer's function in the transaction that removes each
directory, before the removal, so the consumer's row goes with the directory or neither goes. An
error from the function leaves the directory deleting for the next pass. A consumer's reference
to a file refuses the file's purge the same way: the file stays deleting, and each pass reports
the refusal until the reference is gone. A consumer that holds its files before referencing them
keeps the reference-then-delete rule across a branch: the mark waits on a hold as `Files.Delete`
does.

### Stale rows and orphaned objects

Both file protocols can stop partway and never be resumed: a write after its first step leaves a
pending row, and a delete after `Files.Delete` leaves a deleting row, hidden from the listings
yet still holding its name. These are stale rows. With `data.StaleOlderThan(age)`, a pass also
reclaims the stale rows last written longer ago than `age`, oldest first, from the budget the
branches leave. It moves a pending row to deleting at the version the pass read, so a write that
completed in the meantime is left as it is, then deletes the row's object and purges the row. A
deleting row whose caller is still finishing its delete is harmless to finish twice, since each
step is idempotent. Without the option a pass reclaims no stale row.

A sweep deletes an object before it purges the object's row, so no row it removes leaves an
object behind. It cannot close one case: a write whose put lands after its pending row was
reclaimed, or after its branch was swept, leaves an object with no row. The write's `Complete`
then fails, with `blobfs.ErrDeleting` while the row is still deleting and `blobfs.ErrNotFound`
once it is purged. The case is inherent, since the put and the row share no transaction and the
put is the consumer's store's, even when `data.Store.Write` calls it. A consumer bounds it by choosing an age longer than its longest
write, counted from the write's first step, since a write resumed through `Files.Ensure` keeps
its row's `updated_at`; the reclaim then never overtakes a write still running. A branch's sweep
has no age, so a write into a branch being deleted can still lose that race. Either way, a
writer whose `Complete` is refused deletes the object it put, under the key it holds, as
`data.Store.Write` does. An object
whose writer stopped after the put is found only by listing the store's keys, the reconciler the
library defers.

## Ownership stays with the consumer

blobfs has no owner, no unit, and no authorization anywhere in its schema or its API. A consumer
composes ownership through its own tables, at the grain its domain needs:

- at the directory grain, a row that binds a top-level directory to a unit, checked before the
  consumer resolves anything below it;
- at the file grain, a join row that binds one file to a unit, with the consumer's own
  constraints, such as a partial unique index for one active file per unit.

The consumer's check runs around the library's listing, never inside it. A consumer's row that
references a directory is removed with it, through the sweep's hook when a branch is deleted
(see [the consumer's own rows](#the-consumers-own-rows)). For its own read models, blobfs
publishes the column lists of its two entities as patterns, `blobfs.directory_columns` and
`blobfs.file_columns`, which a consumer's statements include so they scan into
`blobfs.Directory` and `blobfs.File`.

## Engines and variants

Every statement the persistence package ships is standard SQL, and the package is complete
alone: any engine `sqlate` has a dialect for runs every operation through it. Three operations
are variation points, where an engine can do better than standard SQL: the tree lock; path
resolution, which the baseline walks one segment per statement, since standard SQL has no
ordered array parameter to walk by; and a file's hold, which the baseline takes with an update
that writes a row version. The `data.Variant` interface names them, with `Serializes`; the
baseline is the standard-tier variant `data.New` binds.

An engine sub-module adds an engine's native forms and its DDL. It ships a `data.Engine`, which
`data.New` calls with the baseline it compiled, and a consumer installs it with
`data.WithEngine`. The PostgreSQL sub-module's variant takes a transaction-scoped advisory lock,
resolves a path of any depth in one statement, and holds a file's row with a locking read that
writes no row version. Each native statement declares its tier and a
port note, the engine feature it uses and what a port to another engine must provide.

The other native forms need no variant. A command that returns its changed row, a create, a
complete, a move, or a delete's first step, is a returning command of `sqlate`, and the dialect
chooses its form when the statement compiles: the single-statement form, with `RETURNING`, on an
engine that has the clause, and elsewhere the fallback, the command and a read of the row in one
transaction. The cursor's
keyset predicate is a pattern an engine's dialect module overlays, so a consumer on PostgreSQL
registers `sqlate/postgres`'s patterns in its catalog in place of `query.Patterns()`.

A consumer can write an engine of its own: a `data.Engine` whose variant embeds the baseline, or
an engine's variant, and overrides the methods it needs. Embedding is the contract, not a
convenience: a release that adds a variation point adds it to the baseline too, so every variant
that embeds one inherits it, and adding a variation point is a minor release. A variant that
implements the interface without embedding is outside the contract. The conformance suite,
`data/datatest`, checks any variant against the baseline's outcomes on a live database.

## One install per configuration

An install is one tree in one database, with objects in one container. blobfs's object names are
fixed, and nothing in the library names a second database, schema, or container: there is no
volume id and no key prefix. A service that serves several isolated trees runs one configuration
per tree, each with its own database and its own container, so an operation on a container
belongs to exactly one tree.
