# Changelog

All notable changes to the PostgreSQL engine (`github.com/standards-lab/blobfs/postgres`) are
documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This
changelog covers this sub-module only; the base module keeps its own.

## [Unreleased]

The first release of the PostgreSQL engine.

### Added

- `Engine`, the `data.Engine` a consumer installs with `data.WithEngine`, and `Variant`, which
  embeds the store's baseline and overrides two variation points: the tree lock, a
  transaction-scoped advisory lock under `TreeLockKey`, derived from `TreeLockName`, and path
  resolution in one recursive statement for any depth. The variant's statements join the store's
  inventory and its `Verify`.
- `Migrations`, blobfs's schema as a `migrate.Set` named `Source` (`blobfs`) with the history
  table `Table` (`blobfs_schema_version`): the directory migration, which seeds the root, and
  the file migration.
- The integration tier: the conformance suite over the baseline and the engine's variant under
  both forms of the returning commands, the constraints and the migration set against a live
  PostgreSQL, and plan-shape and buffer-bound assertions for the listings, the tree walks, and
  the protocol steps.

Requires `github.com/standards-lab/blobfs v0.1.0` and `github.com/standards-lab/sqlate v0.4.0`.

[Unreleased]: https://github.com/standards-lab/blobfs/commits/main/postgres
