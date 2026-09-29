# Features

Every feature of blobfs, package by package, in the detail needed to use it without reading the
source. The [concepts](concepts.md) document explains the model; the package documentation,
`go doc github.com/standards-lab/blobfs/...`, is the reference for each exported name.

## blobfs: the root package

The root package is Go only. It imports neither `sqlate` nor an object store, and its one
dependency outside the standard library is `golang.org/x/text`, for Unicode normalization. A
consumer that keeps its own persistence takes only this package: it authors its own DDL from
[the schema](#the-schema), mints ids with `NewID`, normalizes and validates names before every
insert and rename, builds each key with `NewKey`, and checks status changes with `Transition`.

### Entities

- `Directory` is a row of `blobfs_directory`: `ID`, `ParentID` (`*string`, nil only for the
  root), `Name`, `Status` (a `DirectoryStatus`), `Version`, `CreatedAt`, and `UpdatedAt`.
  `IsRoot` reports whether the row has no parent.
- `File` is a row of `blobfs_file`: `ID`, `DirectoryID`, `Name`, `Status`, `Key`, `Size`
  (`*int64`), `ContentType`, `ETag` (`*string`), `Version`, `CreatedAt`, and `UpdatedAt`. `Size`
  and `ETag` are nil until the write completes; `ContentType` is the type declared at upload
  until `Complete` replaces it with the one the store reported.
- `Object` is what the store reported about a stored object, `Size`, `ContentType`, and `ETag`,
  which the consumer builds from its store's answer to the put and hands to `Complete`.

The `json` tags are the scan and binding contract: the columns carry the same names. `Version`
is the concurrency token every guarded step checks.

`RootID` is the root directory's id, `00000000-0000-0000-0000-000000000000`.

### Ids

`NewID` mints a version 7 UUID in canonical text form, so ids sort in insertion order. `ParseID`
checks an id a caller supplies in place of a minted one and returns its canonical form; text
that is not a UUID, and the nil UUID, which is the root's, are an `IDError`.

### Statuses and transitions

`Status` is a string type with three values, `StatusPending`, `StatusAvailable`, and
`StatusDeleting`, and binds and scans as text without a `Valuer`. `Valid` reports whether a
value is one of the three, and `Mutable` whether a row in that status accepts a move or a
rename, which only `deleting` does not.

`CanTransition(from, to)` reports whether the table allows a change, and `Transition(from, to)`
returns nil or a `TransitionError`:

| From | Allowed to |
|---|---|
| `pending` | `available`, `deleting` |
| `available` | `deleting` |
| `deleting` | `deleting` |

A `TransitionError` carries `From` and `To`. It matches `ErrInvalidTransition` under `errors.Is`
always, and `ErrDeleting` as well when `From` is `deleting`.

`DirectoryStatus` is a directory row's status, a string type with two values,
`DirectoryStatusActive` and `DirectoryStatusDeleting`, which binds and scans as text as `Status`
does. Every directory is active until `Directories.MarkDeleting` marks its branch, and no change
leaves `deleting`, so the type has no transition table. `Valid` reports whether a value is one
of the two. `Mutable` reports whether a directory in that status accepts a create beneath it and
a move into it, out of it, or of itself; only a deleting directory refuses them.

### Keys

`KeyValidator` is the one method a write asks of an object store:
`ValidateKey(key string) error`, non-nil with the reason when the store refuses the key. A
consumer wires its store's own rule to it at the composition root; the [quick
start](quick-start.md#5-write-the-object-store-adapter) shows the adapter over `go-storage`. The
interface carries no maximum length: the store's own validation enforces its limit, counted the
way the store counts.

`SanitizeFilename(name)` turns a display name into a key's filename segment: each invalid UTF-8
sequence, slash, backslash, and control character becomes an underscore, trailing dots and
whitespace are removed, and a name nothing survives of becomes `file`. The result satisfies the
key rules of Azure Blob Storage and S3.

`NewKey(store, id, name)` builds the key, `id + "/" + SanitizeFilename(name)`, and validates it
against the store. A refusal is a `KeyError`, which carries `Key` and the store's error `Err`,
matches `ErrInvalidKey`, and reaches the store's error through `errors.As`.

### Names

`NormalizeName` returns a name in Unicode normalization form C. `ValidateName` accepts a name
that is non-empty, valid UTF-8, at most `MaxNameLength` (255) runes, free of slashes and control
characters, and neither `.` nor `..`; a refusal is a `NameError`, which carries `Name` and
`Reason` and matches `ErrInvalidName`. Validate the normalized form, since normalization can
change the rune count. The persistence package normalizes and validates every name it is given,
before any SQL.

255 runes is the per-component limit ext4, NTFS, and APFS share, so a name copied from a local
disk fits. A key of the longest name is 292 runes, within Azure Blob Storage's limit of 1024
characters. S3 counts its limit of 1024 in bytes of UTF-8, and 255 four-byte runes make a key
of 1057 bytes, so the longest names can exceed it; the store's validator refuses such a key,
and the write is a `KeyError` before any SQL.

### Errors and constraints

The sentinels are listed in [the errors table](#errors). `ViolationError` reports a database
constraint violation a classifier mapped to a sentinel: `Sentinel`, `Constraint`, the violated
constraint's name, and `Err`, the cause. Its message prints the sentinel and the constraint name
and never the driver's text. `Unwrap` yields the sentinel and the cause, so `errors.Is` matches
the sentinel and `errors.As` reaches the `sqlate.ConstraintError` beneath. The persistence
package builds one for each constraint blobfs owns, and a consumer can build one for its own
constraints with its own sentinels.

`DeletingError` is the type in which the persistence package reports `ErrDeleting`, and it says
whose delete refused the mutation. `Directory` is false only for a file whose own delete began, its
row deleting while its directory is active, and `ID` then names the file. Otherwise `Directory` is
true and `ID` names the deleting directory: the file's own directory, for a file its branch's mark
reached, or the directory the mutation reached. `Err` is the cause when there is one: a completion
refused from deleting carries its `TransitionError`, so `ErrInvalidTransition` still matches and
`errors.As` reaches it. The error matches `ErrDeleting` under `errors.Is`, so a caller that only
needs to know a delete is under way is unaffected. A refusal of a deleting file reads the file's
directory to tell the two apart. A directory gone since the file was read, which its foreign key
allows only once the file's row is gone too, as the sweep of its branch leaves them, is reported
as the directory's refusal; a read that fails otherwise leaves the refusal an untyped
`ErrDeleting` beside the read's error.

A name is unique among the rows of one table in a directory, whatever their status, so a deleting
row holds its name until it is purged or removed. A create or a move onto a name a deleting row
holds is refused with that row's `DeletingError`, not `ErrNameTaken`, because the listings hide the
row that holds the name: a file's own, or its directory's once the directory is deleting, and a
directory's with `Directory` true. The error does not wrap `ErrNameTaken`: the refusal is the
delete, which the caller waits out, and not a name the caller must change; a check that matches
`ErrNameTaken` before `ErrDeleting` keeps the answer "name taken" for live rows only. `ErrNameTaken`
remains the refusal of a name a pending or available file, or an active directory, holds. The
statement itself refuses a deleting holder, selecting nothing rather than failing the unique
constraint, and the store then reads the name's holder to report it, so the refusal runs no failing
statement and leaves the caller's transaction usable. The refusal is marked as the holder's, "the
file ID holds the name" or "the directory ID holds the name" around its `DeletingError`, so a move
refused by the name's holder reads apart from one refused by the moved row's own delete (see [the
two-phase delete](concepts.md#the-two-phase-delete)). A row that turns deleting after the statement
ran, whose mark or delete commits in between, is still refused as `ErrNameTaken` by the constraint.

At read committed each statement reads its own snapshot, so the holder the statement saw may be
purged, by the sweep or the stale reclaim, or its name taken by a live row, before the store
reads it. When the reads after a statement that selected nothing find no cause, the store runs
the statement once more: the rerun succeeds, is refused by the constraint, `ErrNameTaken` for a
live row, or selects nothing again with a holder the reads find. Nothing failed, so the rerun is
safe inside the caller's transaction. It runs once only, and a rerun the reads explain no better
is returned as an untyped error naming the row's state.

The names of the constraints the persistence package classifies are constants:

| Constant | Name | Violation means |
|---|---|---|
| `ConstraintPrimaryKeyDirectory` | `blobfs_pk_directory` | `ErrIDTaken` |
| `ConstraintPrimaryKeyFile` | `blobfs_pk_file` | `ErrIDTaken` |
| `ConstraintUniqueDirectoryRoot` | `blobfs_uq_directory_root` | `ErrRootDirectory` |
| `ConstraintUniqueDirectoryParentName` | `blobfs_uq_directory_parent_name` | `ErrNameTaken` |
| `ConstraintUniqueFileDirectoryName` | `blobfs_uq_file_directory_name` | `ErrNameTaken` |
| `ConstraintForeignKeyDirectoryParent` | `blobfs_fk_directory_parent` | `ErrNotFound` on a write, `ErrNotEmpty` on a delete |
| `ConstraintForeignKeyFileDirectory` | `blobfs_fk_file_directory` | `ErrNotFound` on a write, `ErrNotEmpty` on a delete |

A create or a move whose parent or directory does not exist is refused before the foreign key is
reached: the statement selects nothing from a missing or deleting directory, and the store reads
the directory to report `ErrNotFound` or the directory's `DeletingError`. The foreign key
refuses a write, as a `ViolationError`, only when the directory is removed between the
statement's read of it and the write.

## data: the persistence package

`data` holds blobfs's standard-tier statements, the pattern namespace it publishes, and the
`Store`. It depends on the root package and `sqlate`, and on no driver, dialect module, or
object store.

### The store

`New(catalog, dialect, opts...)` compiles the embedded statements against the consumer's catalog
for the dialect and binds them. No I/O happens. The catalog must carry the query library's
patterns, `query.Patterns()` or an engine's overlay of them such as `postgres.Patterns()` from
`sqlate/postgres`, and blobfs's own, `data.Patterns()`; a catalog without the `blobfs` namespace
is refused before compiling, with the fix named. The dialect chooses the form of each returning
command, the create, complete, move, and delete steps that return their row as the database
holds it: the single-statement form where it renders `RETURNING`, and otherwise the fallback,
the command and its read in one transaction, the caller's when the session is a `*sqlate.Tx` and
one of the store's own on the pool.

`WithEngine(e)` installs an engine. Without it the store runs the baseline. With it,
`New` binds the baseline over the statements it compiled and calls `e(catalog, dialect, base)`
once, and the store runs the variant `e` returns; the statements are compiled once either way.
An engine's error is returned as `data: new store: engine: ...`.

`Store` has two handles, `Directories` and `Files`, and these methods:

- `Statements()` returns the compiled inventory in name order, followed by the variant's own
  statements.
- `Verify(ctx, sess)` prepares every statement, and each returning command's single-statement
  form, against the schema the session reaches, and probes both listings' field contracts and a
  page past a cursor. A variant that compiled statements of its own is verified in the same
  pass. A program calls it at startup, after its migrations, so a statement the schema no longer
  satisfies fails there and not at first use.
- `Write`, `Ensure`, `Remove`, and `Purge` run the two-phase write and delete end to end over
  the consumer's object store; see [the protocols](#the-protocols).
- `Sweep(ctx, db, objects, opts...)` runs one bounded pass that finishes the deletes callers
  began, and the package's `SweepUntilDone` runs passes until the work is done; see [the
  sweep](#the-sweep).

Every operation takes the context and the session first and passes the session through
unwrapped, so a call runs on the pool or inside the caller's transaction. The five operations
correct only inside a transaction take a `*sqlate.Tx`: `Directories.Move`,
`Directories.LockTree`, `Directories.MarkDeleting`, `Files.Hold`, and `Files.Delete`. The
`Store`'s own methods take the `*sqlate.DB` itself, since they open transactions of their own.

`AtVersion(v)` is the one option of the calls that act on a row the caller read: `Files.Hold`,
`Files.Delete`, `Directories.Delete`, and `Directories.MarkDeleting`. The call acts only while
the row is at version `v`, and a row at another version is `query.ErrVersionMismatch`. A row
already deleting is `ErrDeleting` to `Files.Hold`, and a retry to `Files.Delete` and
`Directories.MarkDeleting`, at any version (see [deleting outranks the
version](concepts.md#deleting-outranks-the-version)). Its type is `VersionOption`.

### Patterns

`Patterns()` is blobfs's published pattern source under `Namespace`, `blobfs`: the column lists
the entity types scan.

| Pattern | Columns | Correlation name |
|---|---|---|
| `blobfs.directory_columns` | `d.id, d.parent_id, d.name, d.status, d.version, d.created_at, d.updated_at` | `d`: `FROM blobfs_directory d` |
| `blobfs.file_columns` | `f.id, f.directory_id, f.name, f.status, f.key, f.size, f.content_type, f.etag, f.version, f.created_at, f.updated_at` | `f`: `FROM blobfs_file f` |

A consumer's statement includes one as `{{> blobfs.file_columns}}` and scans with
`query.Scanner[blobfs.File]()`. Every pattern is parameter-free, so a projection base that
includes one gains no parameter from it. A consumer's `sqlint.toml` names the module as a
source, `blobfs = "github.com/standards-lab/blobfs"`, and the linter reads the published
directory from the module's own `[export]`.

### Directories

| Method | What it does | Refusals |
|---|---|---|
| `Find(ctx, sess, id)` | Reads a directory by id; the root is `Find` of `RootID`. | `ErrNotFound` |
| `FindByName(ctx, sess, parentID, name)` | Reads the child directory named `name`, normalized first. A file of the same name is not found. | `NameError`, `ErrNotFound` |
| `FindByPath(ctx, sess, startID, path)` | Resolves a relative path, `a/b`, below a directory; the empty path is the start. | `ErrInvalidPath` for a leading slash, an empty segment, a trailing slash, or a refused segment (which also matches `ErrInvalidName`); `ErrNotFound` for a missing start, a file's id, or a segment that names no directory, with the failing prefix in the text |
| `Path(ctx, sess, id)` | Computes the path from the root at read time, `/` for the root and `/a/b` below it, in one recursive statement whose cost is the directory's depth. | `ErrNotFound`; `ErrCycle` when the chain of parents loops |
| `Create(ctx, sess, parentID, name, opts...)` | Inserts a directory and returns the row. `WithID` supplies the id. | `NameError`, `IDError`, `ErrNameTaken` (by an active directory), `ErrIDTaken`, `ErrNotFound` for the parent, `ErrDeleting` for a deleting parent, and a deleting directory's `DeletingError` for a name it holds |
| `Ensure(ctx, sess, parentID, name, opts...)` | Returns the directory with that name, creating it when none exists, and whether this call created it. | as `Create`; a name already held is found, not refused, except in the race below; a found directory that is deleting is `ErrDeleting` |
| `Move(ctx, tx, id, parentID, name, version)` | Moves or renames a directory under the tree lock, after the cycle check, guarded by `version`. | `ErrRootDirectory`, `NameError`, `ErrCycle`, `ErrNotFound` for the directory or the new parent, `ErrNameTaken` (by an active directory), `ErrDeleting` when the directory or either parent is deleting, `query.ErrVersionMismatch`, a deleting directory's `DeletingError` for a name it holds; at repeatable read or serializable, `sqlate.ErrSerializationFailure` |
| `Delete(ctx, sess, id, opts...)` | Removes one empty directory, only at the version `AtVersion` names when it is given. | `ErrRootDirectory`, `ErrNotEmpty`, `ErrReferenced`, `ErrNotFound`, and with `AtVersion`, `query.ErrVersionMismatch` |
| `MarkDeleting(ctx, tx, id, opts...)` | The first step of [a branch's delete](#deleting-a-branch): marks the directory, every directory beneath it, and every file in them deleting, and returns the counts it changed as `Marked`. | `ErrRootDirectory`, `ErrNotFound`, and with `AtVersion`, `query.ErrVersionMismatch` |
| `Deleting(ctx, sess, limit)` | Returns at most `limit` roots of the branches being deleted, in id order. | a `limit` below 1 |
| `IsWithin(ctx, sess, id, ancestorID)` | Reports whether `id` lies in the subtree of `ancestorID`, that directory included; a directory that does not exist is within nothing. On a loop in the tree, `id` is within every directory on its chain and none off it. | none of its own |
| `LockTree(ctx, tx)` | Takes the variant's tree lock in `tx`, held until it ends, for a tree-shape change of the caller's own; `Move` and `MarkDeleting` take it themselves. | the engine's error |
| `Serializes()` | Reports whether `LockTree` serializes across transactions. | none |
| `List`, `Continue` | [Listings](#listings). | |

`Create` never creates a root, since it always binds a parent.

`Ensure` looks the name up first and inserts only when it finds no row, so the common case runs
no failing statement and composes into a caller's transaction. A creator that commits the name
between the lookup and the insert makes the insert fail as `ErrNameTaken`. On the pool the row
is then looked up again and returned as found; inside a transaction the error is returned,
because on PostgreSQL the failed insert has aborted the transaction, and the caller retries the
transaction. A deleting directory committed there fails no statement, since the insert selects
nothing under it, so it is looked up again inside a transaction too, and refused as a found one
is. A found row keeps its own id whatever `WithID` supplied.

`Move` runs `LockTree`, then `IsWithin(parentID, id)`, then the guarded update, in `tx`. The
caller reads the directory in the same transaction and passes its `Version`. The directory's
children and files follow it, because they reference it by id. A file under the new parent with
the same name is no conflict. `IsWithin` is exported for a consumer's own scope check and for
refusing a move early in a user interface: a directory D may move under P only when
`IsWithin(P, D)` is false. Its answer is reliable only while no other transaction moves
directories. The tree lock's guarantee assumes read committed isolation; at repeatable read or
serializable the engine refuses the second of two opposing moves with
`sqlate.ErrSerializationFailure`, and the caller retries it in a new transaction (see
[moves](concepts.md#moves)). `IsWithin` and `Path` terminate on a loop in the tree, with the
answers the table gives, and a `Move` of a directory on the loop back under the root repairs
it.

`Delete` of a directory with children or files is `ErrNotEmpty`, deleting rows included; there
is no cascade. A consumer that wants a directory gone with everything in it marks its branch and
sweeps it (see [deleting a branch](#deleting-a-branch)). A consumer's own foreign key into
`blobfs_directory` refuses the delete as `ErrReferenced`, and a consumer that keeps a row about
the directory removes it in the same transaction as the directory. With `AtVersion`, the
directory is removed only at that version, in the same statement, and a read tells a directory
at another version from a missing one.

On a store whose `Serializes` reports false, [moves](concepts.md#moves) gives the three ways to
make directory moves safe.

### Files

| Method | What it does | Refusals |
|---|---|---|
| `Find(ctx, sess, id)` | Reads a file by id, whatever its status. | `ErrNotFound` |
| `FindByName(ctx, sess, directoryID, name)` | Reads the file named `name` in a directory, whatever its status: the last step of resolving a file's path. A directory of the same name is not found. | `NameError`, `ErrNotFound` |
| `Create(ctx, sess, keys, directoryID, name, contentType, opts...)` | The write's first step: inserts the row as `pending` with its key and the declared content type, and returns it. | `NameError`, `IDError`, `KeyError`, all before any SQL; `ErrNameTaken` (by a pending or available row), `ErrIDTaken`, `ErrNotFound` for the directory, `ErrDeleting` for a deleting directory, and a deleting row's `DeletingError` for a name it holds |
| `Ensure(ctx, sess, keys, directoryID, name, contentType, opts...)` | The retry-safe first step: returns the row that holds the name and a `WriteOutcome`. | as `Create`; a name already held is found, not refused, except in the race `Directories.Ensure` describes |
| `Complete(ctx, sess, id, version, obj)` | The write's last step: moves the pending row to `available`, records `obj`, and returns the row. | `ErrNotFound`; a `DeletingError` wrapping the `TransitionError` when a delete began; `query.ErrVersionMismatch`; or a `TransitionError` matching `ErrInvalidTransition` when the write was already completed |
| `Move(ctx, sess, id, directoryID, name, version)` | Moves or renames a file, guarded by `version`. The key is untouched. | `NameError`, `ErrNotFound` for the file or the directory, `ErrNameTaken` (by a pending or available row), a `DeletingError` when the file, its directory, or the new directory is deleting, `query.ErrVersionMismatch`, a deleting row's `DeletingError` for a name it holds |
| `Hold(ctx, tx, id, opts...)` | Locks the row for the rest of `tx` without changing it. | `ErrNotFound`, a `DeletingError`, and with `AtVersion`, `query.ErrVersionMismatch` |
| `Delete(ctx, tx, id, opts...)` | The delete's first step: moves the row to `deleting`, advancing its version once, and returns it with its key. | `ErrNotFound`, and with `AtVersion`, `query.ErrVersionMismatch` |
| `Purge(ctx, sess, id)` | The delete's last step: removes a deleting row. A row already gone is success. | `ErrNotDeleting`, `ErrReferenced` |
| `List`, `Continue` | [Listings](#listings). | |

`Ensure`'s outcomes:

| `WriteOutcome` | Row found | Next step |
|---|---|---|
| `WriteCreated` | none; a pending row was inserted | put under `Key`, then `Complete` at `Version` |
| `WriteResumed` | a pending row an earlier write left | the same, under the row's own `Key` |
| `WritePresent` | an available or deleting row, returned unchanged | the caller's decision: a put refuses the name, a copy skips or replaces it, a seeder skips it |

The lookup-first behavior inside and outside a transaction is `Directories.Ensure`'s. A found
row keeps its own id and key whatever `WithID` supplied. A deleting row the lookup finds is
`WritePresent`, not refused, and so is one a writer committed between the lookup and the
insert: the insert, which selects nothing under a deleting row and fails no statement, is
followed by the lookup again, inside a transaction too. In a deleting directory, `Ensure`
reports a file the mark reached as `WritePresent`, its row deleting, and refuses a name no row
holds with `Create`'s `ErrDeleting`. A found row that is not deleting is returned without a read
of its directory, so a straggler, a row a create left active in a branch being deleted, is
`WriteResumed` or `WritePresent` like any other; `Directories.Ensure` likewise returns an active
directory it finds under a deleting parent. The sweep removes such a row with its branch (see
[stragglers](concepts.md#stragglers-and-convergence)).

A pending row may be moved: its key is fixed at the insert, and a retry of its write finds it
by its new name. `Complete` tells its refusals apart from the row its returning read returns,
with no further statement, except for a deleting row. For a deleting row, `Complete`, `Move`,
and `Hold` read the file's directory to tell the file's own delete from its branch's (see
[`DeletingError`](#errors-and-constraints)). `Move` also reads the two directories when the row
its returning read returns does not explain its refusal.

`Hold` takes the row's lock that `Delete` waits on, changes no value, and advances no version,
so other holders of the row's version stay valid. It is a variation point, `Variant.HoldFile`,
whose refusals are the same on every variant. A pending row is held like an available one; a
deleting row is refused, since a file whose delete has begun takes no new reference.
`AtVersion(v)` makes the hold match only at version `v`, for a caller that acts on a listing
without reading the row again in its transaction.

`Delete` waits on a `Hold` another transaction took, so once it returns, every reference a hold
admitted has committed; the consumer checks for its own references in `tx`, after the call.
`Purge` refused by a consumer's foreign key leaves the row deleting, with the
`sqlate.ConstraintError` reachable so the consumer matches the constraint's name against its
own.

### The protocols

The `Store` runs the two-phase write and delete end to end over the consumer's object store,
which it reaches only through two interfaces the consumer's adapter satisfies:

- `ObjectPutter`: `PutObject(ctx, key, body, contentType, size) (blobfs.Object, error)` stores
  `size` bytes of `body` under `key` in `contentType`, all or nothing, replacing whatever the key
  held, and reports the stored object as `Complete` records it.
- `ObjectDeleter`: `DeleteObject(ctx, key) error`, as [the sweep](#the-sweep) calls it.

`ObjectStore` combines the two. Each protocol takes the `*sqlate.DB` and runs its first
transaction around a callback of the consumer's, in which the consumer checks its own scope and
writes or removes its own rows. The protocol returns the callback's error, or its transaction's,
as it came, and names every other error once as `data: <op> file <id>: ...`.

| Method | What it does | Refusals |
|---|---|---|
| `Write(ctx, db, objects, body, size, begin)` | Runs `begin`, which calls `Files.Create`, in one transaction, so the pending row commits before any byte; puts `body` under the row's `Key` outside any transaction, in the type the row declares; completes the row on the pool and returns it available. | `begin`'s; before any put, a `TransitionError` for a row that is not pending, wrapped in its `DeletingError` for a deleting one; the put's; `Complete`'s |
| `Ensure(ctx, db, objects, id, body, size, begin)` | The retry-safe `Write` of the file under the fixed `id`, such as a seed's: `begin` calls `Files.Ensure` with `WithID(id)`. Returns the row and whether this call stored its object. | `IDError` before any SQL; `begin`'s; `ErrNameTaken` for a pending or available row found under another id; an error naming both ids for a row `begin` created under another id; a `DeletingError` for a deleting row found, under `id` or another; `Write`'s |
| `Remove(ctx, db, objects, pick, opts...)` | Runs `pick`, which returns the file's id, and `Files.Delete` under `opts` in one transaction, then `Purge`. | `pick`'s; `Files.Delete`'s; `Purge`'s |
| `Purge(ctx, db, objects, file)` | The delete's last two steps, for a file whose `Files.Delete` the consumer ran itself: deletes the object under its `Key`, then purges the row. | the object delete's, which leaves the row deleting; `Files.Purge`'s |

`Write` writes only a pending row. A row `begin` returns in any other status, such as an
available row that `Files.Ensure` found, is refused before any put, and nothing is removed; a
write that may find its row already there is `Ensure`'s.

`Write` never leaves a row behind a failure it can undo. A put or a completion that fails
abandons the write through `Remove`'s steps, at the row's pending version, so the name is free
for a retry and a row another writer moved or completed meanwhile is left as it stands. The
abandon, and the object delete described below, run under `ctx` without its cancellation, so a
caller that hangs up mid-body still cleans up. That context carries no deadline of its own, so
the consumer's object store adapter and its pool bound their own calls. If the abandon fails
too, the row stays pending or deleting, a stale row the sweep reclaims. For that reason `begin`
inserts no row that references the file: a reference would refuse the reclaim's purge. A
completion refused with `ErrDeleting` or `ErrNotFound` means a sweep reached the row before the
put landed; `Write` deletes the object it put, which that sweep could not have deleted, and
leaves the row to the sweep.

`Ensure` differs from `Write` in what it finds:

- A row `Files.Ensure` created, or a pending row an earlier write under `id` left, is written as
  `Write` writes it, and `stored` is true.
- An available row under `id` is returned as it stands, nothing put, and `stored` is false.
- `Ensure` checks the id of the row `begin` returns, whatever it did, before any put. A row found
  under another id is not the caller's, such as a client's upload of the same name; it is left
  as it stands, nothing put and nothing removed, and reported as `ErrNameTaken`, or, when it is
  deleting, as its `DeletingError`, which `Ensure` does not retry. A row `begin`
  created under another id, because it ran `Files.Ensure` without `WithID(id)`, is abandoned at
  its version, nothing put, and reported with both ids named.
- When the first transaction is refused with `ErrNameTaken` or `ErrIDTaken`, because a
  concurrent writer's insert won the race, `Ensure` runs `begin` once more in a fresh
  transaction, which finds the winner's row.
- Two writers may then share a pending row. `Ensure` abandons only at the pending version it
  holds, so it never removes a row the other writer completed. When the other writer completed
  the row first, `Ensure`'s completion, or its abandon, finds a stale version or a row already
  available; it then reads the row back and returns it as found, with `stored` false. A
  completion refused as deleting or not found is no such race and is returned as it came. Both
  puts store the same bytes under the same key, so the object is whole whichever lands last.

The key is the row's id and name, so an `Ensure` under its fixed id after a reset of the tables
but not of the store puts over the object the earlier write left, and completes.

`Remove` begins the delete at any version when `opts` carries none, and a delete begun at any
version is the retry of one already begun. Every step of `Remove` and `Purge` converges on a
retry: a retry of an interrupted `Remove` finishes it, while a retry of a `Remove` that already
finished finds no row and returns `ErrNotFound`. A `Purge` that never runs leaves a deleting row
the sweep's stale reclaim finishes.

### Listings

Each handle lists one directory's contents, anchored on its id, over a projection base of the
query library:

| Handle | Base | Anchor | Declared fields | Nullable |
|---|---|---|---|---|
| `Directories` | `directory_children` | `parentID` | `id`, `parent_id`, `name`, `status`, `version`, `created_at`, `updated_at` | `parent_id` |
| `Files` | `directory_files` | `directoryID` | `id`, `directory_id`, `name`, `status`, `size`, `content_type`, `etag`, `version`, `created_at`, `updated_at` | `size`, `etag` |

The key of both is `name`; the object key is not a declared field. The listing of `RootID` is
the depth-one directories. A directory that does not exist lists no rows and a total of zero.

Both listings hide deleting rows: each appends a filter, `status` not `deleting`, after the
caller's own, so neither the page nor the total holds a deleting row: a directory of a branch
being deleted, or a file whose delete began or whose branch was marked. A filter on the file
listing's `status` narrows it to one stage of the write. The listing of a directory that is
itself deleting is `ErrDeleting`, told by a read of the directory after the page. With
`IncludeDeleting()`, a `ListOption`, a listing shows every status, so a filter on `status`
reaches the delete's stage too, and lists a deleting directory without the read. The option is
how the work of a delete is found.

`Listing[T]` is the interface both listings satisfy: `*Directories` is a
`Listing[blobfs.Directory]` and `*Files` a `Listing[blobfs.File]`. A consumer that reads either
listing the same way takes a `Listing[T]`.

- `List(ctx, sess, id, req, page, opts...)` reads page `page.Number` of `page.Size` rows, both
  at least 1.
- `Continue(ctx, sess, id, req, after, size, opts...)` reads the `size` rows past `after`, the
  `Next` of an earlier page of the same listing, under the same filters and sort.

Both return a `query.Collection[T]`: `Items`, `Total`, `More`, and `Next`. `More` is read as
one row past the page's size.

- **Directives.** `req` is a `query.Directives`: `Sort` (`[]query.Sort`), `Filters`
  (`[]query.Filter`, with the query library's operators), and `Total`. A field the base does not
  declare, an unknown operator, or a value of the wrong shape for its operator is refused before
  any SQL. Each value binds cast to its field's declared type, so a value the engine cannot read
  as that type is refused by the engine as a `query.InvalidValueError`. Every such refusal
  unwraps to `query.ErrDirectives`.
- **Order.** The default order is by name. The library appends `name` as the tie-breaker to
  every sort that does not name it, so every order is total.
- **Cursor.** A page carries `Next` only when `More` is true and its sort is cursorable: every
  term up to the name runs in one direction and names no nullable field. A sort by `parent_id`,
  `size`, or `etag`, or one that mixes directions, pages by number only. A cursor is opaque,
  URL-safe text bound to the listing, its sort, and its filters, and a cursor edited, issued by
  the other listing, or presented under another sort or other filters is refused with a
  `query.CursorError` before any SQL. The appended filter is part of what a cursor is bound to,
  so a cursor continues only a listing called the same way, with `IncludeDeleting` or without
  it; without it, a row marked deleting after the cursor was issued is not on the pages past it.
  A cursor is a position in the order, not a bookmark on the directory: it does not record the
  directory's id.
- **Total.** Under `query.TotalExact`, the zero value, the total is a window count in the page's
  own statement, so it never disagrees with the page, and a continued page's total counts the
  whole listing under the filters, not only the rows past the cursor. An empty first page
  reports 0; an empty page after the first and an empty continued page report `query.NoTotal`.
  Under `query.TotalNone` the read skips the count and reports `query.NoTotal`. The counted read
  holds every filtered row before it pages, so a caller walking a large directory by cursor
  passes `query.TotalNone` after its first page.
- **Cost.** The status filter is a predicate over the rows the name index reaches, not an index
  condition, so a listing's cost is the directory's rows whether or not any is deleting.

### Deleting a branch

A branch is a directory with everything beneath it. [Concepts](concepts.md#deleting-a-branch)
explains the two stages; this section states their rules.

`MarkDeleting(ctx, tx, id, opts...)` runs in `tx` under the tree lock, `LockTree`. One recursive
update marks the directory and every directory beneath it `DirectoryStatusDeleting`, and a
second, over the same walk, marks every file in them `StatusDeleting`. Where `Serializes`
reports true the two walk one branch; on the baseline a concurrent move can reshape it between
them, or while the first waits on a row, and [moves](concepts.md#moves) gives the courses that
prevent it. Each row it changes
advances its version once and has its `updated_at` stamped; a row already deleting is left as it
is. It returns `Marked`, whose `Directories` and `Files` count the rows this call moved to
deleting, so a repeated mark reports only what it reached anew. The walk descends through
directories already deleting, so a repeated mark reaches a straggler: an active row left in the
branch by a create that read its parent before the first mark committed. The file update takes
each row's lock, so it waits on a `Hold` another transaction took. The root is
`ErrRootDirectory` before any SQL, and a directory that does not exist is `ErrNotFound`. With
`AtVersion`, the version guards the first update in the same statement, in its walk's anchor:
an active directory at another version starts no walk and marks nothing, and a directory already
deleting is the mark's retry at any version. When the update changes no row, one read tells a
missing directory, `ErrNotFound`, from one at another version, `query.ErrVersionMismatch`, and
from a retry, after which the files' update runs. The walk costs the size of the branch, never
of the tree, and terminates on a loop in the tree.

After the mark, a deleting directory refuses every operation that would add to the branch or
take from it, with `ErrDeleting`:

| Operation | Refused when |
|---|---|
| `Directories.Create`, `Files.Create` | the parent or the directory is deleting |
| `Directories.Ensure`, `Files.Ensure` | as `Create`, and `Directories.Ensure` also when the directory it finds is deleting |
| `Directories.Move` | the directory, its current parent, or its new parent is deleting |
| `Files.Move` | the file, its current directory, or its new directory is deleting |
| `List`, `Continue` without `IncludeDeleting` | the listed directory is deleting |

`Find`, `FindByName`, `FindByPath`, and `Path` read a deleting directory as any other, and its
`Status` says so. `Complete` and `Hold` of a marked file are refused as for any deleting file.

`Deleting(ctx, sess, limit)` returns the roots of the branches being deleted: each deleting
directory under an active parent, which is the directory a mark named, and none beneath it. The
rest of each branch is reached from its root through the listings with `IncludeDeleting`. It is
the sweep's read, and a consumer's own tool can run it. The PostgreSQL migration indexes the
deleting directories, so the read costs them and not the table.

### The sweep

`Store.Sweep(ctx, db, objects, opts...)` runs one bounded pass that finishes the deletes callers
began and did not complete, and returns a `SweepResult`. `db` is the `*sqlate.DB`, not a
session. The pass reads and purges on the pool and deletes objects outside any transaction, so
it holds no row lock across a call to the store, and it opens one transaction for each mark,
each directory's removal, and each pending row's delete.

`objects` is an `ObjectDeleter`, the consumer's store as the sweep calls it: `DeleteObject(ctx,
key) error`. It must be idempotent, treating a missing object as success, since a pass may
delete an object a stopped pass deleted already, and a pending row's object may never have been
stored. An error leaves that file's row deleting for the next pass.

For each branch root `Deleting` returns, in id order, the pass walks the branch depth first
through the listings with `IncludeDeleting`, each directory's files and then its child
directories a page at a time in name order. In each directory it deletes every file's object and
purges the file's row, empties and removes each child directory the same way, and then removes
the directory itself. Each removal runs in a transaction of its own, guarded by the version the
pass read the directory at. A straggler, an active row the walk meets or a directory refused as
`ErrNotEmpty`, has the pass mark the branch again, in a transaction of its own, and walk the
directory again; a straggler met after that mark stops the branch's walk with `More` set, for
the next pass.

A pass's cost is bounded by `Batch`: it reads the roots, the pages of each directory, and the
stale rows no further than the records its budget allows, plus the rows it leaves in place. A
mark walks its whole branch, and on the PostgreSQL engine holds the tree lock while it does,
blocking every `Move` and `MarkDeleting`; a pass marks a branch only when stragglers appear, at
most once a branch, so a branch with none is swept in passes of `Batch` records with no mark at
all.

| Option | Effect |
|---|---|
| `Batch(n)` | Bounds the records one pass handles: each file whose object it deletes and whose row it purges, each directory it removes, and each stale row it reclaims counts one. The default is 100; an `n` below 1 is refused before any SQL. |
| `OnRemoveDirectory(fn)` | Runs `fn(ctx, tx, dir)` in the transaction that removes each directory, before the removal, with the directory as the pass read it. An error rolls the removal back and leaves the branch for the next pass; `fn` runs again on each attempt, with nothing of an aborted attempt left. |
| `StaleOlderThan(age)` | Also reclaims, from the budget the branches leave, the oldest file rows that are pending or deleting and were last written more than `age` ago: a pending row is moved to deleting at the version the pass read, so a write completed meanwhile is left as it is, then its object is deleted and its row purged. An `age` that is not positive is refused before any SQL. |

`SweepResult` counts what the pass did, each row once, by the step that purged it: `Files`
counts the files of branches, `Directories` the directories removed, and `Stale` the stale rows
reclaimed. `More` reports work the pass did not reach: it stopped at its bound with a page, a
branch, or a read of roots or stale rows not finished, or at a straggler. When the budget runs
out exactly as a read ends, one row read past the rows the pass left tells whether more remain.
A caller runs passes while `More` is true. A stale row in a branch the walk has not reached yet
is finished by the reclaim and counted in `Stale`. Nothing to do is a zero result and no error.

A refusal leaves the row it meets, not the branch or the pass. The refusals are an object
delete's error, the hook's error, and a purge or removal a consumer's foreign key refuses as
`ErrReferenced`. A refused file stays deleting, and the walk goes on with its directory's other
files and child directories; the directories above it cannot be removed while it remains, and
stay for a later pass. A refused directory stays the same way, and a refused root or stale row
is read past, so every later read of the pass starts beyond the rows it left. A refusal spends
no budget and does not set `More`. A file refused in a branch's walk is not tried again in the
pass, by the walk or the stale reclaim. The pass returns every refusal joined and wrapped as
`data: sweep: ...`, with the result counting what it did.

`data.SweepUntilDone(ctx, stop, pass, report)` is the loop a consumer's sweep worker runs. It
runs `pass` while the pass reports `More`, and hands each pass's result and error to `report`,
where the consumer logs them; a nil `report` discards them. `pass` is one pass as the consumer
runs it: a closure that calls `Sweep` with the consumer's session, object store, and options,
inside whatever the consumer holds for a whole pass, such as a gate that orders the sweep
against its schema changes. The loop checks `ctx` and `stop` before each pass. It returns nil
once a pass reports no `More` or once `stop` is closed; a closed `stop` ends the loop between
passes and never interrupts one, and a nil `stop` never closes. It returns `ctx`'s error once
`ctx` ends, before a pass or during one, and does not report a pass that `ctx` ended. A pass's
own error never ends the loop: every step is idempotent and the next pass finds the work in the
database, so `report` judges what the error means. A pass that refuses its options reports no
`More`, so the loop reports that refusal once and ends.

`StaleOlderThan`'s `age` must exceed the longest write the consumer lets run, counted from the
write's first step. The age is measured against the consumer's clock and `updated_at` against
the database's, so their skew is part of the margin. A `Complete` of a reclaimed row is
`ErrDeleting`, or `ErrNotFound` once the row is purged; see [orphaned
objects](concepts.md#stale-rows-and-orphaned-objects).

### Variants and engines

`Variant` is the interface of the variation points:

- `LockTree(ctx, tx)` takes the tree lock, held until `tx` ends. It serializes only the
  transactions that take it.
- `Serializes()` reports whether `LockTree` serializes at all.
- `ResolvePath(ctx, sess, startID, segments)` walks normalized, validated names down from a
  start and returns the deepest directory reached and its depth, the number of segments matched;
  a start that does not exist is `ErrNotFound`.
- `HoldFile(ctx, tx, id, version)` takes the row lock `Files.Delete` waits on, for the rest of
  `tx`, and reports whether it held the row: one that exists, is not deleting, and sits at
  `*version` when `version` is not nil. It changes no value; `Files.Hold` reads the row after a
  false to classify the refusal, so the refusals are the same on every variant.
- `Statements()` and `Verify(ctx, sess)` are the variant's own inventory: the statements it
  compiled beyond the data package's, which `Store.Statements` appends, and their verification,
  which `Store.Verify` runs in its own pass.

The baseline, which `New` binds and passes to an engine as `base`, is a no-op `LockTree`,
`Serializes` false, a walk that reads the start and then one child per segment, a hold that is
an update assigning a column to itself, and no statements of its own. `Engine` is
`func(catalog *query.Catalog, dialect sqlate.Dialect, base Variant) (Variant, error)`.

A variant embeds the variant it is given, `base` or an engine's variant, and overrides the
methods it needs. That is the contract: a release that adds a variation point adds its method to
the baseline as well, so every variant that embeds one inherits it, and adding a variation point
is a minor release. A type that implements `Variant` without embedding one is outside the
contract, and a minor release may break its build. This one wraps the PostgreSQL engine's
variant and logs each path resolution:

```go
// tracing is a variant with its path resolution logged.
type tracing struct{ data.Variant }

func (t tracing) ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error) {
	log.Printf("resolve %d segments below %s", len(segments), startID)
	return t.Variant.ResolvePath(ctx, sess, startID, segments)
}

// tracingEngine builds the PostgreSQL variant over the store's baseline and wraps it.
func tracingEngine(c *query.Catalog, d sqlate.Dialect, base data.Variant) (data.Variant, error) {
	v, err := postgres.Engine(c, d, base)
	if err != nil {
		return nil, err
	}
	return tracing{v}, nil
}
```

`data.New(catalog, dialect, data.WithEngine(tracingEngine))` installs it. `tracing` inherits
the PostgreSQL variant's `Statements` and `Verify` through the embedding, so the engine's
statements stay in the store's inventory and its startup verification.

The store validates every input and classifies every error, so a variant binds what it is given
and returns what the session mapped.

## The schema

The tables, their columns, the constraint and index names, the referential actions, and the
migration set are public API under semantic versioning: from v1.0, a change to any of them is a
major release, and before v1.0 it is a minor release whose changelog marks it breaking. The
PostgreSQL form ships as the `postgres` sub-module's migration set; a consumer on
another migration tool authors the same DDL from this section.

### `blobfs_directory`

| Column | Type | Null | Default |
|---|---|---|---|
| `id` | `uuid` | no | none: minted in Go |
| `parent_id` | `uuid` | yes: the root's | |
| `name` | `text` | no | |
| `status` | `text` | no | `'active'` |
| `version` | `bigint` | no | `1` |
| `created_at` | `timestamptz` | no | `now()` |
| `updated_at` | `timestamptz` | no | `now()` |

| Name | Kind | Rule |
|---|---|---|
| `blobfs_pk_directory` | primary key | `(id)` |
| `blobfs_fk_directory_parent` | foreign key | `parent_id` references `blobfs_directory (id)`, no referential action |
| `blobfs_uq_directory_parent_name` | unique | `(parent_id, name)` |
| `blobfs_cc_directory_name` | check | `name <> ''` |
| `blobfs_cc_directory_parent_not_self` | check | `parent_id <> id` |
| `blobfs_cc_directory_root_name` | check | `(parent_id IS NULL) = (name = '/')` |
| `blobfs_uq_directory_root` | partial unique index | `((parent_id IS NULL)) WHERE parent_id IS NULL`: one root |
| `blobfs_cc_directory_status` | check | `status IN ('active', 'deleting')` |
| `blobfs_ix_directory_deleting` | partial index | `(id) WHERE status = 'deleting'`: the deleting directories alone, which `Deleting` reads |

The migration seeds the root: id `00000000-0000-0000-0000-000000000000`, no parent, name `/`.
The status column, its check, and its index are added by the third migration, which leaves every
existing row active.

### `blobfs_file`

| Column | Type | Null | Default |
|---|---|---|---|
| `id` | `uuid` | no | none: minted in Go |
| `directory_id` | `uuid` | no | |
| `name` | `text` | no | |
| `status` | `text` | no | |
| `key` | `text` | no | |
| `size` | `bigint` | yes, until available | |
| `content_type` | `text` | no | |
| `etag` | `text` | yes, until available | |
| `version` | `bigint` | no | `1` |
| `created_at` | `timestamptz` | no | `now()` |
| `updated_at` | `timestamptz` | no | `now()` |

| Name | Kind | Rule |
|---|---|---|
| `blobfs_pk_file` | primary key | `(id)` |
| `blobfs_fk_file_directory` | foreign key | `directory_id` references `blobfs_directory (id)`, no referential action |
| `blobfs_uq_file_directory_name` | unique | `(directory_id, name)` |
| `blobfs_cc_file_name` | check | `name <> ''` |
| `blobfs_cc_file_status` | check | `status IN ('pending', 'available', 'deleting')` |
| `blobfs_ix_file_stale` | partial index | `(updated_at, id) WHERE status IN ('pending', 'deleting')`: the rows a protocol left partway, oldest first, which the sweep's stale reclaim reads |

### Names and rules

Every object's name has the form `blobfs_<kind>_<table>_<detail>`: kind is `pk`, `fk`, `uq`,
`cc`, or `ix`; table is the table's name without its prefix; detail names the referenced
relation, the columns, or the rule. The prefix `blobfs_` belongs to the set, so a consumer's own
objects take a prefix of their own.

The foreign keys have no cascading action, and a consumer's DDL must not add one. The refusal to
delete a non-empty directory is the foreign key's. A consumer that writes `ON DELETE CASCADE`
removes file rows whose objects still exist, and because no key carries a directory prefix,
those objects are then unreachable.

No index on the key column exists: nothing looks a file up by its key.

### The `created_at` index

The set ships no index on `blobfs_file (directory_id, created_at)`. The unique constraint on
`(directory_id, name)` serves a sort by name, and a sort by `created_at` without the index sorts
the directory. With the index, a page sorted by creation time under `query.TotalNone`, first or
continued by cursor on PostgreSQL's row-value keyset predicate, is an index read whose cost does
not grow with the cursor's position. It buys nothing for a counted page, which reads every row
of the directory for the window count regardless, and it costs storage and write time on every
file row. A consumer that lists by creation time adds it in its own set:

```sql
CREATE INDEX ix_blobfs_file_directory_created ON blobfs_file (directory_id, created_at)
```

### The migration set

| | |
|---|---|
| Name | `blobfs` (`postgres.Source`) |
| History table | `blobfs_schema_version` (`postgres.Table`) |
| Version 1 | `directory`: `blobfs_directory`, its constraints, the root index, and the root row |
| Version 2 | `file`: `blobfs_file` and its constraints |
| Version 3 | `directory_status`: `blobfs_directory.status`, `blobfs_cc_directory_status`, `blobfs_ix_directory_deleting`, and `blobfs_ix_file_stale` |

Each migration ships its down. A released migration never changes in text or name; a change to
seeded data is a new migration. The set references nothing outside the objects it creates, so a
consumer declares it below its own set and references blobfs's tables freely. A second engine
ships the set as a sub-module of its own, with the same file names in its own migrations
directory.

## postgres: the PostgreSQL engine

`github.com/standards-lab/blobfs/postgres` is a sub-module. It imports the persistence package,
the root package, and `sqlate`, and names no driver; a consumer selects it by importing it, with
no registry, init, or flag.

### The engine

`Engine` is a `data.Engine`: it compiles the variant's three native statements against the
consumer's catalog for its dialect and returns the PostgreSQL variant over the baseline
`data.New` compiled. A consumer installs it with
`data.New(catalog, dialect, data.WithEngine(postgres.Engine))`. The catalog must carry the
`blobfs` namespace. The variant embeds the baseline it is given and overrides all three points;
its `Statements` and `Verify` put its statements in the store's inventory and its startup
verification.

- **The tree lock.** `lock_tree` is `pg_advisory_xact_lock` over the fixed key `TreeLockKey`,
  `-8521165719926625175`, the 64-bit FNV-1a hash of `TreeLockName`, `blobfs_directory.tree`,
  read as a signed integer. The lock is transaction-scoped and released when the transaction
  ends; `Serializes` reports true. A consumer that takes advisory locks of its own avoids the
  key.
- **Path resolution.** `resolve_path` walks every segment in one recursive statement that
  indexes a `text[]` parameter by depth, so a path of any depth is one round trip. The segments
  bind as one parameter the driver encodes from the Go slice; no name is spliced into the text.
  The walk is bounded by the segments, so a loop in the tree cannot extend it.
- **The file hold.** `lock_file` is `SELECT ... FOR NO KEY UPDATE` of a row that is not
  deleting, and at the version under `AtVersion`: the lock the baseline's self-assigning update
  takes and `delete_file` waits on, taken without writing a row version, so a hold leaves the
  row's `ctid` and `xmin` as they were and the table gains no dead tuple.
  A refused hold returns no row and takes no lock, and the store reads the row to classify the
  refusal as it does over the baseline.

Each file declares the native tier and carry a port note naming what another engine must
provide. The returning commands need no variant, since sqlate's `postgres.Dialect` renders
`RETURNING`; the keyset predicate needs none, since a consumer registers sqlate's
`postgres.Patterns()` in its catalog for the row-value comparison.

### The migrations

`Migrations()` returns the [migration set](#the-migration-set) as a `migrate.Set`, read from the
embedded directory. A consumer declares it first in `migrate.New`:

```go
blobfsSet, err := postgres.Migrations()
sets := []migrate.Set{blobfsSet, {Name: "app", Migrations: own}}
m, err := migrate.New(db, sets, migrate.Options{})
```

`Up` applies blobfs's set before the consumer's, each under its own history table; blobfs's set
cannot be reverted while the consumer's set above it has applied migrations. A blobfs upgrade
that ships a new migration is applied by the next `Up`.

### What the tiers prove

The unit tier runs with nothing installed. It pins each released migration file's hash, checks
that every object the DDL creates carries the set's prefix and that every constraint constant
names an object in the DDL, recomputes the tree lock's key from its name, and proves the engine
compiles against a consumer's catalog, runs the lock in a transaction, resolves a path in one
statement, and holds a file with the locking read and no write.

The integration tier, behind the `integration` build tag, runs against a live PostgreSQL:

- the conformance suite over the baseline and the engine's variant, under both forms of the
  returning commands, and once more with the standard keyset spelling;
- the tree lock as an advisory lock held until commit, as the engine reports it;
- the file hold writing no row version, its `ctid` and `xmin` unchanged, while it still makes a
  `Delete` wait until the holder ends, where the baseline's hold moves the row to a new version;
- two opposing directory moves at repeatable read, over both variants, refused at the second
  move's update with a serialization failure and leaving no cycle;
- the set beneath a consumer's set: the order of `Up`, the refused revert, `Reset`, and a
  replay;
- every named constraint and index as the engine reports its violation;
- plan shapes and buffer bounds: listing pages under each total mode, deleting rows hidden, a
cursor page over a consumer's `created_at` index at any position, the upward walks' cost bounded
by depth, every protocol step found through the primary key, the read of the branch roots
through `blobfs_ix_directory_deleting`, and the stale read through `blobfs_ix_file_stale` in the
index's order, with no sort.

## datatest: the conformance suite

`datatest.Run(t, db, catalog, engine)` runs the checks every `data.Store` must pass against a
live database, as subtests of `t`. It is how an engine sub-module, or a consumer with an engine
of its own, proves its variant:

- `db` is a throwaway database with blobfs's migration set applied; `Run` is called once per
  database. Its dialect is the one the store is compiled for, and a dialect that renders
  `RETURNING` covers the single-statement form while one that does not covers the fallback.
- `catalog` is the consumer's catalog, carrying `data.Patterns()` beside the query library's
  patterns or an engine's overlay of them.
- `engine` is the engine under test; nil is the baseline.

The suite builds a second store over the baseline on the same database and asserts, wherever an
outcome belongs to a variation point or a returning command, the same rows and the same
refusals, in text, for the same inputs. The concurrency checks assert what `Serializes` reports:
that the lock blocks a second mover, or that two opposing moves on a variant without a lock form
the cycle, that `IsWithin` and `Path` terminate on it with their defined answers and a `Move`
repairs it, and that serializable isolation refuses one of them on every variant. The concurrent
`Ensure` checks force the race they test: the suite holds each caller's lookup until both have
looked, so both insert and one recovers from the refused insert. It creates two
tables of its own, `datatest_reference` and `datatest_owner`, with foreign keys into blobfs's
tables, to stand in for a consumer's references, and an index on
`blobfs_file (directory_id, created_at)`, which it drops again.

The suite is engine-agnostic: it imports no engine and no driver, and every statement it runs
outside the store is standard SQL with the dialect's placeholders. The groups, in order, are
Verify, Directories, Paths, Files, Writes, Deletes, Protocols, Holds, Moves, Listing, Keyset,
Branches, and Sweeps.

Protocols runs the protocols end to end over an in-memory object store. It checks
`Store.Write`'s success; a failed put, which frees the name; a completion refused by a mark made
during the put, after which the write deletes the object it put and leaves the row to the sweep;
a refusing begin; and the refusal of an available row. It checks `Store.Ensure` creating,
returning an available row, resuming a pending one, and refusing a name held under another id
or a deleting row; a second `Ensure` completing the shared row during the first one's put; and
an insert lost to a row a competing writer committed, which `Ensure` resumes. It checks
`Store.Remove`, its failed object delete and retry, and a refusing pick; `Store.Purge` after the
caller's own `Files.Delete`; and `SweepUntilDone` draining a backlog larger than one batch in
counted passes, and running no pass once stopped. Protocols runs before any group marks a
branch, and it sweeps the branches it marks itself.

Branches checks the mark's counts, its convergence and stragglers, its version guard and its
retry at any version, its wait on a hold, every refusal a deleting directory makes, the
listings' hiding, and `Deleting`'s roots; it runs after the other groups because the branches it
marks stay in the tree. Sweeps runs last because its first pass removes them; it then checks a
full sweep with its hook, a pass stopped between an object's delete and its row's purge,
stragglers, the batch bound, a hook that aborts a removal, a refused branch and a refused stale
row that hold back nothing behind them, at the default batch and at `Batch(1)`, the stale
reclaim's age, and concurrent passes converging. Every check of a refusal of a deleting row also
checks the `DeletingError`'s kind: the file's own for a file whose delete began, and the
directory's for everything a mark refuses.

Beside the suite, `FileRows(files...)` and `DirectoryRows(dirs...)` build the `sqltest.Response`
of a read of blobfs's rows, in the columns the store's statements scan, for a consumer's unit
tests over the query library's scripted driver. A nil `Size`, `ETag`, or `ParentID` becomes
NULL, and a call with no rows scripts a read that finds none.

## Errors

Each sentinel is in the root package and is matched with `errors.Is`. The errors of the query
library that a step returns are listed after them.

| Sentinel | Returned when |
|---|---|
| `ErrNotFound` | A read finds no row; a parent or directory an insert or move names does not exist; a step targets a file that does not exist; a mark targets a directory that does not exist; a path segment names no directory. |
| `ErrNameTaken` | A directory or file of the same kind that is not deleting already holds the name in the target directory. A deleting row holds its name too, and is reported as its `DeletingError` (see [errors and constraints](#errors-and-constraints)). |
| `ErrInvalidName` | A name `ValidateName` refuses; a `NameError` carries the reason. |
| `ErrInvalidPath` | A path that starts with a slash, has an empty segment or a trailing slash, or has a segment `ValidateName` refuses. |
| `ErrRootDirectory` | A delete, move, or rename of the root, or a second row without a parent. |
| `ErrInvalidKey` | The store refuses a file's key; a `KeyError` carries the key and the store's reason. |
| `ErrInvalidID` | An id supplied through `WithID` that is not a UUID, or is the nil UUID. |
| `ErrIDTaken` | An id supplied through `WithID` that a row of the same table already carries. |
| `ErrNotEmpty` | A directory delete while the directory has child directories or files, deleting ones included. |
| `ErrInvalidTransition` | A status change the table does not allow, such as `Complete` of a row already available; a `TransitionError` carries the statuses. |
| `ErrDeleting` | `Complete`, `Move`, or `Hold` of a deleting row, whatever version the caller holds; a create or an ensure under a deleting directory, a move into one or out of one, and the move of a deleting directory; a create or a move onto a name a deleting row holds; a listing of a deleting directory without `IncludeDeleting`. Each is a `DeletingError` naming whose delete refused. |
| `ErrNotDeleting` | `Purge` of a row whose delete has not begun. |
| `ErrReferenced` | A directory `Delete` or a file `Purge`, or the sweep's removal or purge, refused by a foreign key blobfs does not own: a consumer's row references the row. |
| `ErrCycle` | A directory move under the directory itself or one of its descendants; `Path` of a directory whose chain of parents loops. |

| Query library error | Returned when |
|---|---|
| `query.ErrVersionMismatch` | A guarded step, `Complete`, a `Move`, or a call with `AtVersion`, finds the row at another version and not deleting; the text carries both versions. |
| `query.ErrDirectives` | A listing request names an undeclared field, an unknown operator, a malformed value, or a page number or size below 1. |
| `query.CursorError` | `Continue` refuses a cursor: malformed, issued elsewhere, or under a sort that cannot be continued. |

A violation of one of blobfs's constraints is a `ViolationError`, so `errors.As` reaches the
constraint's name and the `sqlate.ConstraintError`. A violation of a constraint blobfs does not
own returns unclassified on a write, wrapped with the operation's context, for the consumer to
classify by its own names.
