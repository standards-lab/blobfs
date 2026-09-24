# Changelog

All notable changes to `github.com/standards-lab/blobfs` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html). This changelog covers the base module only;
the `postgres` sub-module keeps its own.

## [Unreleased]

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

[Unreleased]: https://github.com/standards-lab/blobfs/commits/main
