# Changelog

All notable changes to the PostgreSQL engine (`github.com/standards-lab/blobfs/postgres`) are
documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This
changelog covers this sub-module only; the base module keeps its own.

## [Unreleased]

### Changed

- The module requires `blobfs` v0.5.0, whose conformance it runs. The plan test covers the
  `create_file` and `create_directory` statements and reads the branch roots through
  `Directories.BranchRoots`, blobfs v0.5.0's name for `Directories.Deleting`. The package
  documentation names every exported identifier. The engine's behavior is unchanged, and v0.3.0
  works with blobfs v0.5.0 as released.

## [v0.3.0] - 2026-09-29

### Added

- The integration tier runs the conformance suite's Protocols group, which checks the write,
  ensure, remove, purge, and sweep-loop protocols against PostgreSQL in both returning-command
  forms and over both variants.

### Changed

- The scripted test of the hold also scripts the read of the file's directory, which blobfs now
  runs when it refuses a deleting file, to tell the file's own delete from its branch's.
- The module requires `blobfs` v0.3.0, whose protocols and typed deleting refusal it proves, and
  `sqlate` v0.4.1, as the base module does.

## [v0.2.0] - 2026-09-28

The schema for the delete of a branch.

### Added

- Migration 0003, `directory_status`, which adds the column `blobfs_directory.status`, `text
  NOT NULL DEFAULT 'active'`, leaving every existing row active, and the check
  `blobfs_cc_directory_status`, `status IN ('active', 'deleting')`. It also adds two partial
  indexes for the sweep's reads: `blobfs_ix_directory_deleting` on `blobfs_directory (id)` of
  the deleting directories, and `blobfs_ix_file_stale` on `blobfs_file (updated_at, id)` of the
  pending and deleting files. The migration ships its down, which drops each of the four by name
  and re-activates every deleting directory, a branch marked or half swept included, while its
  files stay deleting. The golden test pins both files.
- Migration 0003 runs in one transaction, and neither its index builds nor its check's
  validation is concurrent. The `ALTER TABLE` locks `blobfs_directory` against reads and writes
  while the check reads every row, and each index build locks its table, `blobfs_directory` or
  `blobfs_file`, against writes while it reads every row; each lock lasts until the migration
  commits, a time that grows with the tables. A consumer with large tables applies it in a
  maintenance window.
- The integration tier: the conformance suite's new groups, the directory status check's
  violation, and plan assertions for the reads through the two new indexes.

### Changed

- **Breaking:** the engine runs against blobfs v0.2.0, whose statements read the status column,
  so a consumer applies migration 0003 before it runs the new version; `Up` applies it with the
  rest of the set.
- `resolve_path` returns the directory's status with its other columns, as
  `blobfs.directory_columns` now lists them.
- **Breaking:** `Engine` takes its baseline as a `data.Variant`, as blobfs v0.2.0's
  `data.Engine` declares.
- **Breaking:** `Variant` is unexported. `Engine` returns the variant as a `data.Variant`, which
  lists and verifies its own statements through `Statements` and `Verify`.
- **Breaking:** `lock_file` takes a nullable `version`, which `data.AtVersion` binds and which
  guards nothing when NULL, and `lock_file_at_version` is folded into it: the variant's
  inventory no longer lists it.

Requires `github.com/standards-lab/blobfs v0.2.0` and `github.com/standards-lab/sqlate v0.4.0`.

## [v0.1.0] - 2026-09-24

The first release of the PostgreSQL engine.

### Added

- `Engine`, the `data.Engine` a consumer installs with `data.WithEngine`, and `Variant`, which
  embeds the store's baseline and overrides its three variation points: the tree lock, a
  transaction-scoped advisory lock under `TreeLockKey`, derived from `TreeLockName`; path
  resolution in one recursive statement for any depth; and the file hold, a
  `SELECT ... FOR NO KEY UPDATE` that takes the row lock a delete waits on without writing a row
  version. The variant's statements join the store's inventory and its `Verify`.
- `Migrations`, blobfs's schema as a `migrate.Set` named `Source` (`blobfs`) with the history
  table `Table` (`blobfs_schema_version`): the directory migration, which seeds the root, and
  the file migration.
- The integration tier: the conformance suite over the baseline and the engine's variant under
  both forms of the returning commands, the single-statement form and the fallback; the
  constraints and the migration set against a live PostgreSQL; the hold writing no row version
  while it still makes a delete wait; opposing directory moves at repeatable read refused
  without a cycle; and plan-shape and buffer-bound assertions for the listings, the tree walks,
  and the protocol steps.

Requires `github.com/standards-lab/blobfs v0.1.0` and `github.com/standards-lab/sqlate v0.4.0`.

[Unreleased]: https://github.com/standards-lab/blobfs/compare/postgres/v0.3.0...HEAD
[v0.3.0]: https://github.com/standards-lab/blobfs/releases/tag/postgres/v0.3.0
[v0.2.0]: https://github.com/standards-lab/blobfs/releases/tag/postgres/v0.2.0
[v0.1.0]: https://github.com/standards-lab/blobfs/releases/tag/postgres/v0.1.0
