# Changelog

All notable changes to `github.com/standards-lab/blobfs` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html). This changelog covers the base module only;
the `postgres` sub-module keeps its own.

## [Unreleased]

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

[Unreleased]: https://github.com/standards-lab/blobfs/compare/v0.3.0...HEAD
[v0.3.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.3.0
[v0.2.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.2.0
[v0.1.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.1.0
