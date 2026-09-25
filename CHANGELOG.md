# Changelog

All notable changes to `github.com/standards-lab/blobfs` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html). This changelog covers the base module only;
the `postgres` sub-module keeps its own.

## [Unreleased]

## [v0.2.0] - 2026-09-25

The delete of a branch: a directory with everything beneath it is marked deleting in one
transaction and removed by a bounded, stateless sweep, which also reclaims the rows a stopped write
or delete left. It needs the `postgres` sub-module's migration 0003, which `postgres v0.2.0`
ships.

### Added

- `DirectoryStatus`, with `DirectoryStatusActive` and `DirectoryStatusDeleting`, `Valid`, and
  `Mutable`, and `Directory.Status`.
- `Directories.MarkDeleting`, the first step of a branch's delete: it marks the directory, every
  directory beneath it, and every file in them deleting under the tree lock, in a transaction,
  and returns `Marked`, the counts it changed. A repeated mark converges and reaches a straggler a
  racing create left active in the branch.
- `Directories.Deleting`, the roots of the branches being deleted, in id order.
- `Store.Sweep`, one bounded pass that deletes each marked branch's objects through the
  consumer's `ObjectDeleter`, purges its file rows, and removes its directories deepest first,
  with `SweepResult` (`Files`, `Directories`, `Stale`, `More`) and the options `Batch`,
  `OnRemoveDirectory`, the hook for a consumer's own rows about a removed directory, and
  `StaleOlderThan`, which reclaims pending and deleting file rows older than an age. A refusal
  stops the branch or row it meets and is returned joined with the others; the pass goes on.
- `AtVersion` guards `Files.Delete`, `Directories.Delete`, and `Directories.MarkDeleting` as it
  guards `Files.Hold`; its type is `VersionOption`, with `HoldOption` and `DeleteOption` its
  aliases.
- `ListOption` and `IncludeDeleting`, which list every status and a deleting directory.
- The statements `mark_directory_deleting`, `mark_directory_files_deleting`, `deleting_branches`,
  `stale_files_before`, `delete_directory_at_version`, and `delete_file_at_version`, in the
  store's inventory and its `Verify`.
- `data/datatest`: the groups Branches and Sweeps, which run after the others.

### Changed

- **Breaking:** the published pattern `blobfs.directory_columns` includes `d.status`, so a
  consumer's statement that includes it needs the migration that adds the column, and a scan into
  a type of its own needs the field.
- **Breaking:** a deleting directory is closed. A create or an ensure under it, a move into or out
  of it, and a move of it are `ErrDeleting`, whatever the caller's version.
- **Breaking:** the listings hide deleting rows, directories and files alike, by a filter on
  status appended after the caller's own, and the listing of a deleting directory is
  `ErrDeleting`; `IncludeDeleting` restores the whole listing. A directory that does not exist
  still lists empty. The directory listing declares `status`.
- **Breaking:** a create or a move whose parent or directory does not exist is a plain
  `ErrNotFound`, told by a read of the directory, and no longer a `ViolationError` naming the
  foreign key; the foreign key reports it only when the directory is removed between the read and
  the write.

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

[Unreleased]: https://github.com/standards-lab/blobfs/compare/v0.2.0...HEAD
[v0.2.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.2.0
[v0.1.0]: https://github.com/standards-lab/blobfs/releases/tag/v0.1.0
