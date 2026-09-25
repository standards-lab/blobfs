# Changelog

All notable changes to the PostgreSQL engine (`github.com/standards-lab/blobfs/postgres`) are
documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This
changelog covers this sub-module only; the base module keeps its own.

## [Unreleased]

## [v0.2.0] - 2026-09-25

The schema for the delete of a branch.

### Added

- Migration 0003, `directory_status`, which adds the column `blobfs_directory.status`, `text
  NOT NULL DEFAULT 'active'`, leaving every existing row active, and the check
  `blobfs_cc_directory_status`, `status IN ('active', 'deleting')`. It also adds two partial
  indexes for the sweep's reads: `blobfs_ix_directory_deleting` on `blobfs_directory (id)` of
  the deleting directories, and `blobfs_ix_file_stale` on `blobfs_file (updated_at, id)` of the
  pending and deleting files. The migration ships its down, and the golden test pins both files.
- The integration tier: the conformance suite's new groups over the baseline and the engine's
  variant; the directory status check as the engine reports its violation; and plan-shape and
  buffer-bound assertions for the read of the branch roots through
  `blobfs_ix_directory_deleting`, and for the stale read through `blobfs_ix_file_stale` in the
  index's order with no sort.

### Changed

- **Breaking:** the engine runs against blobfs v0.2.0, whose statements read the status column,
  so a consumer applies migration 0003 before it runs the new version; `Up` applies it with the
  rest of the set.
- `resolve_path` returns the directory's status with its other columns, as
  `blobfs.directory_columns` now lists them.
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

[Unreleased]: https://github.com/standards-lab/blobfs/compare/postgres/v0.2.0...HEAD
[v0.2.0]: https://github.com/standards-lab/blobfs/releases/tag/postgres/v0.2.0
[v0.1.0]: https://github.com/standards-lab/blobfs/releases/tag/postgres/v0.1.0
