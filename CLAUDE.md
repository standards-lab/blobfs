# blobfs

blobfs is a Go library that keeps a tree of directories and file metadata in SQL over any object
store: the rows live in the database, and the blobs live in the consumer's store under opaque
keys. Each engine's DDL and native forms live in a sub-module named for the engine; `postgres` is
the only one, and a second waits on a consumer that needs one. It is a standalone library any Go
project can adopt, built by the Standards Lab organization. The repository is managed with the
marathon workflow; start from `context/README.md`.

## Where the documentation lives

The user guide is this repository's own: `README.md` is the index, and the `docs/` documents
are read in the order it lists. The library is adjacent to the organization's Go Elemental
standard rather than a member of it, and the organization's
[architecture repository](https://github.com/standards-lab/architecture) names it as such in
the standard's catalog. `context/` records only working knowledge the guide and the code do not
express; do not restate documented design here. A change that alters documented behavior
updates the guide in the same effort.

## Repository specifics

- **Module layout.** The base module is rooted at `github.com/standards-lab/blobfs`, with
  `blobfs` at its root, the persistence package `data`, and its conformance suite
  `data/datatest`. Each engine is a sub-module named for it: `postgres` holds the engine's
  variant, its native statements, its migration set, and the integration tier. `example` is a
  module of its own that composes the library with `go-storage` and is never imported.
- **Dependency line.** The base module takes `sqlate` and `golang.org/x/text` and nothing else:
  no driver, no dialect module, no object store. An engine's driver and dialect enter only
  through its sub-module; `go-storage` enters only through `example`. `mise run split-check`
  enforces the import boundaries between the layers.
- **Local development** uses the committed root `go.work`. In the steady state each `go.mod`
  pins released `require` versions. A `replace` directive is temporary: it points a sub-module
  at unreleased base changes, and the release removes it.
- **Tests.** The unit tier runs with nothing installed. The integration tier,
  `mise run integration`, runs the conformance suite over the baseline and each engine's variant
  against the compose stack and is not part of CI. `mise run acceptance` starts the stack, runs
  the integration tier and the example, and resets the stack.
- **Releases, CI, tasks** follow the organization's engineering conventions, the Go Elemental
  principles in the architecture repository: `v*` and `postgres/v*` tags, a per-module CI
  matrix, mise tasks over the modules.
- **Public repo.** Modules resolve through the public Go proxy; CI has no private-module
  configuration.
