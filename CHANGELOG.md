# Changelog

All notable changes to `github.com/standards-lab/blobfs` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html). This changelog covers the base module only;
the `postgres` sub-module keeps its own.

## [Unreleased]

## [v0.8.0] - 2026-11-12

Configuration is one value, not variadic option functions or a long parameter list: every call
of `data` that took options takes a required trailing options struct, whose zero value is the
call's default, and the write protocols, the file create, and the moves take their arguments as
one request value. The `sqlate` requirement moves to v0.7.0, which makes the same change to its
transactions.

### Added

- `data.Options`, which configures `New`. `Engine` installs an engine; the zero value binds the
  baseline.
- `data.CreateOptions`, which configures `Directories.Create`, `Directories.Ensure`,
  `Files.Create`, and `Files.Ensure`. `ID` supplies the id of the row the call inserts, checked
  with `blobfs.ParseID` before any SQL; an empty `ID` mints one.
- `data.VersionOptions`, which configures `Files.Hold`, `Files.Delete`, `Directories.Delete`,
  `Directories.MarkDeleting`, and `Store.RemoveFileID`. `Version` guards the call; a zero
  `Version` leaves it unguarded, since row versions start at 1.
- `data.ListOptions`, which configures `Directories.List`, `Directories.Continue`, `Files.List`,
  and `Files.Continue`. `IncludeDeleting` lists deleting rows too; the zero value hides them.
- `data.CursorPage`, the page a `Continue` reads past a cursor: `After`, the cursor, and `Size`.
  An empty `After` and a `Size` below 1 are refused before any SQL, with sqlate's errors.
- `data.RemoveOptions`, which configures `Store.RemoveFile`. `Version` guards the delete as
  `VersionOptions.Version` does, and `OnDelete` is the hook the `OnDelete` option set; nil runs
  none.
- `data.SweepOptions`, which configures `Store.Sweep`. `Batch` bounds a pass, zero meaning 100;
  `OnRemoveDirectory` is the hook the option of that name set, nil running none; and
  `StaleOlderThan` makes a pass reclaim stale rows older than it, zero reclaiming none. A
  negative `Batch` or `StaleOlderThan` is refused before any SQL.
- `data.FileSpec`, the file `Files.Create` and `Files.Ensure` write: `DirectoryID`, `Name`, and
  `ContentType`, all required.
- `data.FileMove`, where `Files.Move` puts the file: `DirectoryID`, `Name`, and `Version`, all
  required.
- `data.DirectoryMove`, where `Directories.Move` puts the directory: `ParentID`, `Name`, and
  `Version`, all required.
- `data.WriteFileRequest`, the write `Store.WriteFile` runs: `DB`, `Objects`, `Body`, `Size`,
  `Begin`, and `OnComplete`, the hook the `OnComplete` option set, nil running none.
  `WriteFile` panics before any I/O with `data: WriteFileRequest.DB is nil`, or the same message
  naming `Objects`, `Body`, or `Begin`, checked in that order. An interface holding a nil
  pointer is set.
- `data.EnsureFileRequest`, the write `Store.EnsureFile` runs: `WriteFileRequest`'s fields with
  the fixed `ID`. `EnsureFile` panics as `WriteFile` does, naming `EnsureFileRequest`, before it
  checks the `ID`; an invalid or empty `ID` stays a returned `IDError`.

### Changed

- **Breaking:** The `sqlate` requirement is v0.7.0. `DB.Begin`, `DB.Transact`, and `Transact`
  take a trailing `*sql.TxOptions` in place of variadic `TxOption` values, and `TxOption`,
  `Isolation`, and `ReadOnly` are removed. sqlate's changelog migrates each. The store opens its
  own transactions with nil, a default transaction, as before.
- **Breaking:** `data.New(catalog, dialect, opts)` takes a `data.Options` in place of variadic
  `data.Option` values.
- **Breaking:** `Directories.Create` and `Directories.Ensure` take a trailing
  `data.CreateOptions` in place of variadic `data.CreateOption` values.
- **Breaking:** `Files.Create(ctx, sess, keys, spec, opts)` and
  `Files.Ensure(ctx, sess, keys, spec, opts)` take a `data.FileSpec` in place of their
  `directoryID`, `name`, and `contentType` parameters, and a trailing `data.CreateOptions` in
  place of variadic `data.CreateOption` values.
- **Breaking:** `Files.Move(ctx, sess, id, to)` takes a `data.FileMove` in place of its
  `directoryID`, `name`, and `version` parameters, and `Directories.Move(ctx, tx, id, to)` a
  `data.DirectoryMove` in place of its `parentID`, `name`, and `version` parameters.
- **Breaking:** `Files.Hold`, `Files.Delete`, `Directories.Delete`, `Directories.MarkDeleting`,
  and `Store.RemoveFileID` take a trailing `data.VersionOptions` in place of variadic
  `data.VersionOption` values.
- **Breaking:** `Directories.List` and `Files.List` take a trailing `data.ListOptions` in place
  of variadic `data.ListOption` values, and `Directories.Continue` and `Files.Continue` take a
  `data.CursorPage` in place of their `after` and `size` parameters and a trailing
  `data.ListOptions`. The `data.Listing[T]` interface has both new signatures, so a consumer's
  own implementation changes with them.
- **Breaking:** `Store.WriteFile(ctx, req)` takes a `data.WriteFileRequest` in place of its
  `db`, `objects`, `body`, `size`, and `begin` parameters and variadic `data.WriteOption`
  values, and `Store.EnsureFile(ctx, req)` a `data.EnsureFileRequest` in place of the same and
  its `id`. A nil `Body`, which reached the put before, now panics.
- **Breaking:** `Store.RemoveFile` takes a trailing `data.RemoveOptions` in place of variadic
  `data.RemoveOption` values.
- **Breaking:** `Store.Sweep` takes a trailing `data.SweepOptions` in place of variadic
  `data.SweepOption` values. A zero `Batch` or `StaleOlderThan`, which the option functions
  refused, now means the default.
- An empty `data.CreateOptions.ID` mints an id, where `WithID("")` was refused as an `IDError`.
- `data/datatest`'s subtests `Deletes/AtVersion` and `Branches/MarkAtVersion` are named
  `Guarded` and `MarkGuarded`, after the field; a `-run` filter naming the old names changes.

### Removed

- **Breaking:** `data.Option` and `data.WithEngine`, with no aliases. Set `Options.Engine`.
- **Breaking:** `data.CreateOption` and `data.WithID`, with no aliases. Set `CreateOptions.ID`.
- **Breaking:** `data.VersionOption` and `data.AtVersion`, with no aliases. Set
  `VersionOptions.Version`, or `RemoveOptions.Version` for `Store.RemoveFile`.
- **Breaking:** `data.ListOption` and `data.IncludeDeleting`, with no aliases. Set
  `ListOptions.IncludeDeleting`.
- **Breaking:** `data.WriteOption` and `data.OnComplete`, with no aliases. Set
  `WriteFileRequest.OnComplete` or `EnsureFileRequest.OnComplete`.
- **Breaking:** `data.RemoveOption` and `data.OnDelete`, with no aliases. Set
  `RemoveOptions.OnDelete`.
- **Breaking:** `data.SweepOption` and the `data.Batch`, `data.OnRemoveDirectory`, and
  `data.StaleOlderThan` option functions, with no aliases. Set `SweepOptions.Batch`,
  `SweepOptions.OnRemoveDirectory`, and `SweepOptions.StaleOlderThan`.

### Migrating from v0.7.0

- **Each option function becomes a field of the call's options struct.** Pass the struct's zero
  value where no option was given; leave a field unset for its default.

  | v0.7.0 | v0.8.0 |
  |---|---|
  | `data.WithEngine(e)` | `data.Options{Engine: e}` |
  | `data.WithID(id)` | `data.CreateOptions{ID: id}` |
  | `data.AtVersion(v)` | `data.VersionOptions{Version: v}`, or `data.RemoveOptions{Version: v}` for `RemoveFile` |
  | `data.IncludeDeleting()` | `data.ListOptions{IncludeDeleting: true}` |
  | `data.OnComplete(fn)` | `WriteFileRequest.OnComplete` or `EnsureFileRequest.OnComplete`: `fn` |
  | `data.OnDelete(fn)` | `data.RemoveOptions{OnDelete: fn}` |
  | `data.Batch(n)` | `data.SweepOptions{Batch: n}` |
  | `data.OnRemoveDirectory(fn)` | `data.SweepOptions{OnRemoveDirectory: fn}` |
  | `data.StaleOlderThan(age)` | `data.SweepOptions{StaleOlderThan: age}` |

  ```go
  // v0.7.0
  store, err := data.New(catalog, dialect)
  store, err := data.New(catalog, dialect, data.WithEngine(blobfspg.Engine))
  dir, err := store.Directories.Create(ctx, db, parentID, "reports", data.WithID(id))
  err := store.Files.Hold(ctx, tx, id)
  marked, err := store.Directories.MarkDeleting(ctx, tx, id, data.AtVersion(v))
  page, err := store.Files.List(ctx, db, dirID, req, query.Page{Number: 1, Size: 20})
  err := store.RemoveFile(ctx, db, objects, pick, data.AtVersion(v), data.OnDelete(fn))
  result, err := store.Sweep(ctx, db, objects, data.Batch(50), data.StaleOlderThan(time.Hour))

  // v0.8.0
  store, err := data.New(catalog, dialect, data.Options{})
  store, err := data.New(catalog, dialect, data.Options{Engine: blobfspg.Engine})
  dir, err := store.Directories.Create(ctx, db, parentID, "reports", data.CreateOptions{ID: id})
  err := store.Files.Hold(ctx, tx, id, data.VersionOptions{})
  marked, err := store.Directories.MarkDeleting(ctx, tx, id, data.VersionOptions{Version: v})
  page, err := store.Files.List(ctx, db, dirID, req, query.Page{Number: 1, Size: 20},
  	data.ListOptions{})
  err := store.RemoveFile(ctx, db, objects, pick, data.RemoveOptions{Version: v, OnDelete: fn})
  result, err := store.Sweep(ctx, db, objects,
  	data.SweepOptions{Batch: 50, StaleOlderThan: time.Hour})
  ```

- **A file's create, the moves, and `Continue` take their arguments as one value.** The
  positional parameters become fields of the same meaning; the row's own id and `keys` stay
  positional.

  ```go
  // v0.7.0
  file, err := store.Files.Create(ctx, tx, objects, dirID, name, contentType)
  file, outcome, err := store.Files.Ensure(ctx, tx, objects, dirID, name, contentType,
  	data.WithID(id))
  file, err := store.Files.Move(ctx, tx, id, dirID, name, version)
  dir, err := store.Directories.Move(ctx, tx, id, parentID, name, version)
  next, err := store.Files.Continue(ctx, db, dirID, req, page.Next, 20, data.IncludeDeleting())

  // v0.8.0
  spec := data.FileSpec{DirectoryID: dirID, Name: name, ContentType: contentType}
  file, err := store.Files.Create(ctx, tx, objects, spec, data.CreateOptions{})
  file, outcome, err := store.Files.Ensure(ctx, tx, objects, spec, data.CreateOptions{ID: id})
  file, err := store.Files.Move(ctx, tx, id,
  	data.FileMove{DirectoryID: dirID, Name: name, Version: version})
  dir, err := store.Directories.Move(ctx, tx, id,
  	data.DirectoryMove{ParentID: parentID, Name: name, Version: version})
  next, err := store.Files.Continue(ctx, db, dirID, req, data.CursorPage{After: page.Next, Size: 20},
  	data.ListOptions{IncludeDeleting: true})
  ```

- **The write protocols take one request value.** Each parameter becomes a field of the same
  meaning, and `OnComplete` a field, nil for none.

  ```go
  // v0.7.0
  file, err := store.WriteFile(ctx, db, objects, body, size, begin)
  file, err := store.WriteFile(ctx, db, objects, body, size, begin, data.OnComplete(fn))
  file, stored, err := store.EnsureFile(ctx, db, objects, id, body, size, begin)

  // v0.8.0
  file, err := store.WriteFile(ctx, data.WriteFileRequest{
  	DB: db, Objects: objects, Body: body, Size: size, Begin: begin,
  })
  file, err := store.WriteFile(ctx, data.WriteFileRequest{
  	DB: db, Objects: objects, Body: body, Size: size, Begin: begin, OnComplete: fn,
  })
  file, stored, err := store.EnsureFile(ctx, data.EnsureFileRequest{
  	DB: db, Objects: objects, ID: id, Body: body, Size: size, Begin: begin,
  })
  ```

- **A nil `DB`, `Objects`, `Body`, or `Begin` on a write request panics.** Each is a wiring
  mistake, and the call panics naming the field before any I/O. A nil `body`, which v0.7.0 passed
  to the put, now panics, so an empty file passes `http.NoBody` or an empty reader such as
  `strings.NewReader("")` as its `Body`.

- **Zero values that were refused are now valid.** `data.Batch(0)` was refused, and a zero
  `SweepOptions.Batch` means 100. `data.StaleOlderThan(0)` was refused, and a zero
  `SweepOptions.StaleOlderThan` reclaims no stale rows, as a sweep without the option did.
  `data.WithID("")` was refused as an `IDError`, and an empty `CreateOptions.ID` mints an id. A
  negative `Batch` or `StaleOlderThan` is still refused, and `EnsureFileRequest.ID` still refuses
  the empty id.

- **`AtVersion(0)` becomes an unguarded call.** `data.AtVersion(0)` guarded the call at a
  version no row has, so it matched no row and failed as `query.ErrVersionMismatch`; `VersionOptions{}` and
  `RemoveOptions{}` leave the call unguarded. A caller that passed a version read from a row
  passes it unchanged.

- **A zero `FileMove.Version` or `DirectoryMove.Version` is still a guard.** A move is always
  guarded: a zero `Version` matches no row and the call fails as `query.ErrVersionMismatch`,
  as a zero `version` parameter did. Pass the version read from the row.

- **sqlate's own transactions change too.** sqlate's changelog migrates `DB.Begin`,
  `DB.Transact`, `Transact`, and a `Beginner`'s `Begin` to a trailing `*sql.TxOptions` from
  v0.6.x, so a consumer's `sqlate.Transact(ctx, db, fn)` becomes
  `sqlate.Transact(ctx, db, fn, nil)` and `sqlate.Isolation(sql.LevelSerializable)` becomes
  `&sql.TxOptions{Isolation: sql.LevelSerializable}`.

## [v0.7.0] - 2026-10-15

### Added

- `data.OnComplete`, the one `data.WriteOption`, which `Store.WriteFile` and `Store.EnsureFile`
  take as a trailing variadic argument. Its function runs in the completion's transaction, after
  `Files.Complete`, with the call's context and the completed `File`, so a consumer's rows, such
  as its reference to the file, commit exactly when the file does. An error from the function
  rolls the completion back and abandons the write as a failed completion does, and the call
  returns it wrapped. `EnsureFile` runs the function only when the call completes the file
  itself: never for a row it finds available, or one another writer completed first. A caller
  that passes no option compiles and behaves as before.
- `data.OnDelete`, a `data.RemoveOption` that `Store.RemoveFile` takes. Its function runs in the
  transaction that runs `pick` and `Files.Delete`, after the file is marked deleting, with the
  call's context and the `File` as marked, so a consumer's rows commit exactly when the delete
  begins. A retry, whose delete finds the file already deleting, runs no function and finishes
  the delete. An error from the function rolls the transaction back, so the file is not marked
  and nothing is purged, and the call returns it wrapped.
- `data/datatest` checks the completion hook. The Protocols group checks that the hook's writes
  commit with the row (`WriteOnComplete`) and that its error rolls them back and leaves no file
  (`WriteOnCompleteRefuses`), and, for `EnsureFile`, that the hook runs once for the completing
  call (`EnsureOnComplete`), not at all over an available row
  (`EnsureOnCompleteOverAnAvailableRow`) or for a writer whose completion lost
  (`EnsureOnCompleteOnASharedRow`), and that its error, even one wrapping
  `query.ErrVersionMismatch`, rolls back and leaves no file (`EnsureOnCompleteRefuses`).
- `data/datatest` checks the delete hook. The Protocols group checks that it runs once and its
  writes commit (`RemoveOnDelete`), that a `RemoveFile` of a file already deleting runs none
  (`RemoveOnDeleteRetried`), and that its error leaves the file as it was with its object in
  place (`RemoveOnDeleteRefuses`).

### Changed

- **Breaking:** `Store.RemoveFile` takes a trailing `...data.RemoveOption` in place of
  `...data.VersionOption`. `data.RemoveOption` is an interface that `data.VersionOption`, and so
  `data.AtVersion`, satisfies, so a call passing no option or `AtVersion(v)` compiles unchanged;
  only a caller spreading a `[]data.VersionOption` into `RemoveFile` must build a
  `[]data.RemoveOption` instead.
- `Store.WriteFile` and `Store.EnsureFile` complete the file in a transaction of their own on
  `db`, hooked or not, where they completed it on the pool. Their outcomes are unchanged.
- `Store.EnsureFile` tells a completion another writer won from the completion's and the
  abandon's own refusals rather than from the error it returns, so a hook's error that wraps
  `query.ErrVersionMismatch` or `blobfs.ErrInvalidTransition` is returned, not taken for that
  race. Without a hook its outcomes are unchanged.
- The features and concepts documents state how a consumer that runs its own work in a branch
  mark's transaction tells its mark from a retry: it reads the anchor's status under
  `Directories.LockTree` before `Directories.MarkDeleting`.

## [v0.6.0] - 2026-10-09

### Changed

- **Breaking:** The `sqlate` requirement is v0.5.0, whose `query.Scanner` returns every
  `time.Time` in `time.UTC`. Every `File` and `Directory` the store returns, from a read, a
  listing, a create, or a protocol, carries `CreatedAt` and `UpdatedAt` in `time.UTC`, whatever
  `time.Local` is. Under v0.5.0 they kept the location the driver gave them, which with pgx is
  `time.Local`. The instant is unchanged. A listing's cursor carries a keyed time as UTC text, and
  a cursor issued before this release still reads back as the same instant.

## [v0.5.0] - 2026-09-30

### Added

- `Store.RemoveFileID`, the two-phase delete of a file the caller names by id, for a caller with
  no scope to check and no reference to remove in the delete's transaction.
- `data/datatest` checks new races. The Directories and Writes groups race two `Ensure` calls
  that supply one id (`EnsureConcurrentUnderOneID`), and the Branches group races a file move
  out of a branch against the branch's mark in both orders (`MarkRacesAFileMove`). The Protocols
  group also checks that a retry of a finished `RemoveFile` is `ErrNotFound`.

### Changed

- **Breaking:** the `Store`'s protocols are named for the file they act on: `Write` is
  `WriteFile`, `Ensure` is `EnsureFile`, `Remove` is `RemoveFile`, and `Purge` is `PurgeFile`.
  Their signatures and behavior are unchanged.
- **Breaking:** `ObjectStore` embeds `blobfs.KeyValidator`, so the one adapter a consumer passes
  to `WriteFile` and `EnsureFile` also implements `ValidateKey`, as the adapter `begin` passes
  to `Files.Create` or `Files.Ensure` already did. `Files.Create` and `Files.Ensure` keep their
  `KeyValidator` parameter, since a consumer that runs the steps itself needs no put or delete.
- **Breaking:** `Directories.Deleting` is `Directories.BranchRoots`, named for what it returns;
  its error reads `data: branch roots: ...`.
- Each package's documentation names every exported identifier by concept and states the
  package-level contracts. Each symbol's documentation states its own contract, names its
  refusals briefly, and links `docs/features.md`, which holds the refusals, the races, and the
  protocols in full. The statement headers keep why each clause is there and drop the
  clause-by-clause narration.

### Fixed

- `Files.Ensure` and `Directories.Ensure` on the pool recover when two callers supply one id
  through `WithID` and race. PostgreSQL checks the primary key before the name's constraint, so
  the loser's insert failed as `ErrIDTaken`. The loser now looks the name up again and returns
  the row found when it carries the id supplied. Otherwise `ErrIDTaken` stands, joined to the
  second lookup's error when that lookup fails. Inside a transaction the violation is returned,
  as before.
- `Directories.Delete`, `Directories.MarkDeleting`, and `Directories.Move` refuse the root
  before any SQL under every spelling of the nil UUID PostgreSQL accepts, braced or with its
  hyphens placed otherwise. Previously they caught only `RootID`'s canonical text.
- `Files.Complete`'s documentation: a retry at the pending version after its completion
  committed is `query.ErrVersionMismatch`, since the completion advanced the version;
  `ErrInvalidTransition` is the refusal of a row available at the version named.
- `Store.RemoveFile`'s documentation: a retry of a finished `RemoveFile` returns `ErrNotFound`,
  where it said every step converges on a retry.
- The README and the concepts document name the three interfaces the store reaches the object
  store through, and the README's example writes through `Store.WriteFile`; both said the
  library reached the store only through a key check and the sweep's delete.

### Removed

- **Breaking:** `blobfs.CanTransition`; `blobfs.Transition` returns nil for an allowed change.
- **Breaking:** `Status.Valid` and `DirectoryStatus.Valid`, which nothing called.

## [v0.4.0] - 2026-09-29

### Changed

- **Breaking:** a create or a move onto a name that a deleting row holds is refused with that
  row's `*blobfs.DeletingError`, where it was `ErrNameTaken`. A deleting row is a file whose write
  failed both its put and its object's delete in the abandon, a file whose delete stopped before
  its purge, or a directory a mark reached. The listings hide it, so `ErrNameTaken`
  named a row the caller could not see. The change applies to `Files.Create`, `Files.Move`,
  `Directories.Create`, the insert in `Directories.Ensure`, and `Directories.Move`, and so to
  `Store.Write`'s `begin`. A file holder is reported as the file's own `DeletingError`, or as its
  directory's once the directory is deleting; a directory holder is reported with `Directory`
  true. The error does not match `ErrNameTaken`, so a caller that checks `ErrNameTaken` first now
  reaches its `ErrDeleting` branch, and `Store.Ensure` does not retry the error as a concurrent
  writer's. `ErrNameTaken` remains the refusal of a name that a pending or available file, or an
  active directory, holds, still as a `ViolationError` over the unique constraint. The store wraps
  the holder's refusal in "the file `<id>` holds the name" or "the directory `<id>` holds the
  name", so a move refused by the name's holder reads apart from one refused by the moved row's
  own delete;
  `errors.As` still reaches the `DeletingError`, whose `ID` names the holder.
- `Store.Ensure` reports a deleting row found under another id as its `DeletingError`, where it
  was `ErrNameTaken`. `Files.Ensure` returns a deleting row as `WritePresent`, inside a
  transaction too, whether its lookup found the row or a writer committed the row after the
  lookup and the row refused the insert. `Directories.Ensure` likewise looks such a directory up
  again and refuses it as it refuses a found one.
- `create_file`, `create_directory`, `move_file`, and `move_directory` select nothing when a
  deleting row holds the name, rather than failing the unique constraint, so the refusal runs no
  failing statement and leaves the caller's transaction usable. The store then reads the name's
  holder to report it; the success path runs no extra statement. A row that turns deleting after
  the statement ran is still refused by the constraint as `ErrNameTaken`. When the reads find no
  cause, because the holder was purged or a live row took its name after the statement's
  snapshot, the store runs the statement once more. The rerun succeeds, meets the constraint, or
  selects nothing again with a holder the reads find; only a rerun that the reads still do not
  explain returns an untyped error.
- `data/datatest`: the create, move, sweep, and protocol groups assert the `DeletingError` for a
  name a deleting row holds, and the Protocols group adds `WriteUnderADeletingName` and
  `EnsureNameHeldByADeletingRow`.

## [v0.3.0] - 2026-09-29

### Added

- `Store.Write`, which runs the two-phase write end to end: the consumer's callback creates the
  pending row with `Files.Create` in one transaction, the store puts the object outside any
  transaction, and then completes the row. A row that is not pending is refused before any put.
  A failed put or completion abandons the write at the row's pending version, and a completion
  refused because a sweep reached the row deletes the object just put.
- `Store.Ensure`, the retry-safe `Write` of a file under a fixed id, which resumes a pending row,
  returns an available one, and shares a row with a concurrent writer. It checks the id of every
  row its callback returns: a row found under another id is `ErrNameTaken`, and a row created
  under another id is abandoned and refused naming both ids.
- `Store.Remove`, which runs the two-phase delete end to end around the consumer's callback, and
  `Store.Purge`, which runs the object delete and the purge for a delete the consumer began
  itself.
- `ObjectPutter`, the interface over the consumer's put, and `ObjectStore`, which combines it
  with `ObjectDeleter` and is what `Store.Write` and `Store.Ensure` take.
- `SweepUntilDone`, which runs a consumer's sweep pass while the pass reports `More`, stops when
  a stop channel closes, and reports each pass. The pass is the consumer's closure over
  `Store.Sweep`, so whatever the consumer holds for a whole pass, such as a gate or a lock,
  stays under its control.
- `Listing[T]`, the interface `*Directories` and `*Files` satisfy.
- `blobfs.DeletingError`, the type in which `data` reports `ErrDeleting`: it tells a file whose own
  delete began from a deleting directory, names the row, and unwraps its cause.
- `data/datatest`: `FileRows` and `DirectoryRows`, which script reads of blobfs's rows for
  `sqltest`'s driver, and checks of the `DeletingError`'s kind in the groups that assert
  `ErrDeleting`.
- `data/datatest`: the Protocols group, which checks `Store.Write`, `Store.Ensure`,
  `Store.Remove`, `Store.Purge`, and `SweepUntilDone` over `Store.Sweep` against the live
  database, over an in-memory object store that fails on demand.

### Changed

- **Breaking:** `data` reports `ErrDeleting` as a `*blobfs.DeletingError`, whose message names the
  file or the directory, unless the read that decides its kind fails. `errors.Is(err, ErrDeleting)`
  still holds, and `Complete`'s refusal of a deleting row wraps its `TransitionError`, which
  `errors.As` still reaches. `Complete`, `Move`, and `Hold` run one more read when they refuse a
  deleting file: a read of the file's directory. A directory gone by then is reported as the
  directory's `DeletingError`.
- The base module requires `sqlate` v0.4.1.

## [v0.2.0] - 2026-09-28

The delete of a branch, a directory with everything beneath it: the branch is marked deleting in
one transaction and removed by a bounded, stateless sweep, which also reclaims the rows a stopped
write or delete left. It needs the `postgres` sub-module's migration 0003, which `postgres
v0.2.0` ships.

### Added

- `DirectoryStatus`, with `DirectoryStatusActive` and `DirectoryStatusDeleting`, `Valid`, and
  `Mutable`, and `Directory.Status`.
- `Directories.MarkDeleting`, the first step of a branch's delete, which marks a directory with
  everything beneath it deleting and returns `Marked`, the counts of rows it changed.
- `Directories.Deleting`, the roots of the branches being deleted, in id order.
- `Store.Sweep`, one bounded pass that removes marked branches through the consumer's
  `ObjectDeleter`, with `SweepResult` and the options `Batch`, `OnRemoveDirectory`, and
  `StaleOlderThan`, which reclaims stale pending and deleting file rows.
- `AtVersion` guards `Files.Delete`, `Directories.Delete`, and `Directories.MarkDeleting` as it
  guards `Files.Hold`. Its type is `VersionOption`.
- `ListOption` and `IncludeDeleting`, which makes a listing show every status and list a
  deleting directory.
- The statements `mark_directory_deleting`, `mark_directory_files_deleting`,
  `deleting_branches`, and `stale_files_before`, in the store's inventory and its `Verify`.
- `data/datatest`: the groups Branches and Sweeps, which run after the others.

### Changed

- **Breaking:** `HoldOption` is replaced by `VersionOption`, which `Files.Hold`,
  `Files.Delete`, `Directories.Delete`, and `Directories.MarkDeleting` take.
- **Breaking:** the statements `delete_directory`, `delete_file`, and `hold_file` take a
  nullable `version`, which `AtVersion` binds and which guards nothing when NULL, and
  `hold_file_at_version` is folded into `hold_file`: the store's inventory no longer lists it.
- **Breaking:** `Engine` takes its baseline as a `Variant`:
  `func(catalog *query.Catalog, dialect sqlate.Dialect, base Variant) (Variant, error)`.
- **Breaking:** `Standard` is unexported. The baseline reaches an engine only as its `base`
  argument, which a variant embeds through the `Variant` interface.
- **Breaking:** `Variant` gains `Statements() []query.Statement` and
  `Verify(ctx, sess) error`, the variant's own inventory, which `Store.Statements` lists and
  `Store.Verify` runs. A variant that embeds the one it is given gains both.
- **Breaking:** the published pattern `blobfs.directory_columns` includes `d.status`, which
  needs migration 0003 and a field in a consumer's own scan type.
- **Breaking:** a deleting directory is closed: a create or an ensure under it, a move into or
  out of it, and a move of it are `ErrDeleting`.
- **Breaking:** the listings hide deleting rows, and the listing of a deleting directory is
  `ErrDeleting`, unless called with `IncludeDeleting`. The directory listing declares `status`.
- **Breaking:** a create or a move whose parent or directory does not exist is a plain
  `ErrNotFound`, no longer a `ViolationError`, unless the directory is removed mid-statement.

## [v0.1.0] - 2026-09-24

The first release: a SQL-backed tree of directories and file metadata over an object store the
library never calls.

### Added

- `blobfs`, the root package: the entities `Directory`, `File`, and `Object`; `RootID`, the
  seeded root's id; `Status` with `StatusPending`, `StatusAvailable`, and `StatusDeleting`, and
  the transition table behind `CanTransition` and `Transition`; `NewID` and `ParseID`;
  `KeyValidator`, `SanitizeFilename`, and `NewKey`, which builds a file's opaque key,
  `<id>/<sanitized name>`; `NormalizeName`, `ValidateName`, and `MaxNameLength`; the sentinel
  errors, the typed errors `NameError`, `KeyError`, `IDError`, and `TransitionError`, and
  `ViolationError`, which names a sentinel and the constraint that produced it; and the
  constraint-name constants.
- `data`, the persistence package: `New` compiles blobfs's standard-tier statements once against
  the consumer's catalog into a `Store`, with `Statements` and `Verify`. `Patterns` publishes
  the entities' column lists under the `blobfs` namespace.
- `Directories`: `Find`, `FindByName`, `FindByPath`, `Path`, `Create`, `Ensure`, `Move` under
  the tree lock and a cycle check, `Delete` of an empty directory, `IsWithin`, `LockTree`, and
  `Serializes`.
- `Files`: the two-phase write (`Create` or `Ensure`, then `Complete`), the two-phase delete
  (`Delete`, then `Purge`), `Hold` for the reference-then-delete rule, `Find`, `FindByName`, and
  `Move`. `WithID` supplies a row's id, and `AtVersion` guards a hold.
- Listings of one directory's child directories or files, `List` by page and `Continue` by
  cursor, over sqlate projections whose counted total never disagrees with its page.
- `Variant`, the interface of the variation points an engine may override: the tree lock, path
  resolution, and a file's hold (`HoldFile`). A variant embeds the variant it is given, the
  baseline or an engine's, so adding a variation point is a minor release. `Engine`, which builds
  a variant over the baseline; `WithEngine`, which installs one; and `Standard`, the baseline,
  whose hold is a self-assigning update.
- `Complete` and `Move` of a file report `ErrDeleting` for a deleting row whatever version the
  caller holds, as `Hold` does, since `Delete` advances the version past the one a writer or a
  mover read.
- `IsWithin` and `Path` terminate on a loop in the tree, the cycle two opposing moves can leave
  on a variant that does not serialize: `IsWithin` answers by the chain, and `Path` reports
  `ErrCycle`.
- `data/datatest`, the conformance suite: `Run` checks a store over any engine against the
  baseline on a live database, the hold's refusals and interleavings with a delete included.

[Unreleased]: https://github.com/standards-lab/blobfs/compare/v0.8.0...HEAD
[v0.8.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.8.0
[v0.7.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.7.0
[v0.6.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.6.0
[v0.5.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.5.0
[v0.4.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.4.0
[v0.3.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.3.0
[v0.2.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.2.0
[v0.1.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.1.0
