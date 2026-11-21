# goal · harness.adoption-blobfs

- **State:** building
- **Task:** adopt
- **Branch:** adopt

## Tasks

1. [ ] adopt

## Task brief · adopt

**Problem**

blobfs doesn't run marathon's lints, and lint-context fails on its prose capability map. About
2,000 comments in Go, scripts, and configuration are outside the five kinds. Suppressions can go
without reasons. One roadmap pointer is stale. Its check rewrites `go.work.sum` despite saying it
writes nothing. Four pins trail.

**Behaviors**
1. `mise run currency` exits 0. The stack runs PostgreSQL 19.0-alpine and Azurite 3.38.0, and
   `mise run acceptance` passes on it. The quick start shows the same pins. The example module's
   CHANGELOG records its requirement change under Unreleased, if it keeps one; otherwise none.
2. `mise run check` leaves the tree clean: the committed `go.work.sum` is the one the workspace
   resolves.
3. The mise pin `github:standards-lab/claude-plugins` 0.22.0 (prefix `marathon/v`) puts both lints
   on PATH, and currency reports a newer marathon tag.
4. The check also runs lint-context and lint-references and fails when either fails. A lint missing
   from PATH fails the check with a message naming `mise install`. The check's description and the
   README's check line name everything it runs. CI installs the mise tools and passes.
5. golangci-lint enables nolintlint with require-explanation and require-specific. Every remaining
   `//nolint` names its linter and gives a reason.
6. STANDARDS.md has a comment rule citing the five kinds for source, tests, scripts, SQL, and every
   configuration file. It says a SQL file's header is its package documentation (in sqlate's
   format, also template metadata) and names released migrations as frozen, byte-identical.
7. Every Go comment is one of the five kinds. Package comments and exported docs stay; unexported
   docs, test-function docs, and body comments go. Reasoning with no other home moves into names,
   exported docs, or `docs/`. Behavior and exported docs are unchanged.
8. Every SQL, script, and configuration comment is one of the five kinds, or is a SQL file header,
   or sits in a frozen released migration. `--|` lines stay. currency.sh's sections become named
   functions. Reasoning in configuration moves to task descriptions or behind reference comments.
9. lint-context passes. The capability map has `(built)` entries for the root package, data with
   datatest, the postgres engine, and the example. deferred.md keeps the deferred items, and its
   Assumptions move to a `docs/` page.
10. The deferred MySQL item points at `standards-lab/roadmap/index.toml` (backlog
    `second-providers`).

**Test seams**
- `mise run check`, `mise run currency`, `mise run acceptance`, CI on the pull request.

**Slices**
1. Upgrade, and a check that leaves the tree clean.
2. Config, scripts, SQL, STANDARDS rule, nolintlint.
3. Go comments: root package, postgres module, example.
4. Go comments: data source.
5. Go comments: data tests.
6. Go comments: datatest protocols, writes, holds, deletes, files, refusals, rows, fixtures, the
   suite entry.
7. Go comments: datatest sweeps, concurrency, listing, branches, directories, moves, keyset,
   listing branches, paths.
8. Context: map, Assumptions to `docs/`, pointer.
9. Pin, check, CI.

**Out of scope**

Releases (changes stay under Unreleased), exported docs, a budget for `docs/`, a Markdown link
check, feature work.

**Door**

Two-way: nothing is tagged, and the change is local to blobfs.

## Progress

slices 0/9 committed · standards — · spec — · editor —

## Decisions

- adopt: released PostgreSQL migrations stay byte-identical, prose included; the golden sha256
  pins don't change. New migrations follow the five kinds. (wave 1, B1)
- adopt: SQL file headers are package documentation and stay. (wave 1, W1)
- adopt: CHANGELOG entries record only changes to a module's `go.mod` requirements. (wave 1, W2)

## Pending edits

None.
