# blobfs

A virtual tree of directories and files in SQL over any object store: the metadata lives in the
database, the bytes live in the store under opaque keys, and the library imports no object store
and reaches the consumer's only through interfaces the consumer implements.

`github.com/standards-lab/blobfs` is the base module: the root package `blobfs`, the persistence
package `data`, and its conformance suite `data/datatest`, over `sqlate` and `golang.org/x/text`
alone. The PostgreSQL engine, with its native statements and the schema's migration set, is the
`postgres` sub-module, `github.com/standards-lab/blobfs/postgres`, released on its own tags.
`example` is a module of its own that composes the library with a real object store; it is never
imported.

## The problem

An object store is flat and has no atomic rename, so a tree encoded in its keys turns a move of
a directory into a copy and a delete of everything beneath it. blobfs keeps the tree in SQL,
where a move is one update, and leaves each key opaque. The rows and the objects share no
transaction, so blobfs exposes each protocol that touches an object as steps the consumer runs
its own store's call between. The `data` package's store also runs each protocol end to end,
calling the put and delete the consumer supplies; [concepts](docs/concepts.md) explains the
model.

## Documentation

1. [Concepts](docs/concepts.md): the tree over opaque keys, the write and delete protocols,
   moves, listings, the delete of a branch and its sweep, and how engines and ownership fit
   around the library.
2. [Quick start](docs/quick-start.md): a working program from `go get` to a composition against
   PostgreSQL and an object store, tested without a database and against one.
3. [Features](docs/features.md): every operation, refusal, and schema object, package by
   package, in the detail needed to use them without reading the source.
4. [Glossary](docs/glossary.md): the vocabulary the library introduces, by layer.

The package documentation, `go doc github.com/standards-lab/blobfs/...`, is the reference for
each exported name.

## How it works

The library has three layers, and a consumer takes as many as it needs. The root package is Go
only: the entities, statuses, key and name rules, and errors. The persistence package, `data`,
holds blobfs's standard-tier SQL and runs it through `sqlate` on any engine sqlate has a dialect
for. An engine sub-module adds the engine's native forms and ships the schema as a migration
set.

A consumer builds one pattern catalog, compiles the store against it with the engine installed,
and runs the two-phase write through the store's protocol. `objects` is the consumer's adapter
over its object store, a `data.ObjectStore`; error handling is elided.

```go
catalog, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
store, err := data.New(catalog, db.Dialect(), data.WithEngine(blobfspg.Engine))

// The write: the pending row, the object under the row's key, the row available.
file, err := store.WriteFile(ctx, db, objects, body, size, func(tx *sqlate.Tx) (blobfs.File, error) {
	return store.Files.Create(ctx, tx, objects, dirID, "Q3 summary.txt", "text/plain")
})

// One page of the directory's files, with the total counted in the page's own statement.
page, err := store.Files.List(ctx, db, dirID, query.Directives{}, query.Page{Number: 1, Size: 20})
```

The callback runs in the transaction that inserts the pending row, where the consumer checks
its own scope. `file.Key` is `<id>/Q3 summary.txt`. A later rename of the file, or a move of its
directory, updates rows and leaves the object where it is. The protocol's steps are exported
too: a write that does not fit its shape runs `Files.Create`, its own put, and `Files.Complete`
itself.

## Conventions

Four conventions the library keeps are stricter than a reader might expect:

- The base module depends on `sqlate` and `golang.org/x/text` and nothing else: no driver, no
  dialect module, no object store. An engine's driver and dialect enter only through its
  sub-module, and an object store only through the consumer's own adapter.
- The schema is public API under semantic versioning: from v1.0, the tables, columns,
  constraint names, referential actions, and migration set change only in a major release.
  Before v1.0, a minor release may change them, and its changelog marks the change breaking. A
  released migration never changes.
- The library imports no object store. It reaches the consumer's through three one-method
  interfaces, which one adapter implements as `data.ObjectStore`: every write asks whether the
  store accepts a key, the store's write protocols put the object, and its delete protocols and
  the sweep delete it. The protocols' steps, which a consumer may run itself, never touch the
  store.
- Every statement in the base module is standard tier, and the persistence package is complete
  on any engine. A native form lives only in an engine sub-module, and each native file names
  the feature it uses and what a port to another engine must provide.

## Packages

- `blobfs` (the root) is the model: `Directory`, `File`, and `Object`, `RootID`, the status
  vocabulary and its transitions, `NewKey` and `SanitizeFilename`, `NormalizeName` and
  `ValidateName`, `KeyValidator`, the sentinel errors, `ViolationError`, and the constraint
  names.
- `data` is the persistence package. `New` compiles the `Store`. The store's `Directories` and
  `Files` handles run the operations and listings. Its protocols, `WriteFile`, `EnsureFile`,
  `RemoveFile`, `RemoveFileID`, and `PurgeFile`, run the write and the delete end to end over
  the consumer's `ObjectStore`, and its `Sweep` finishes the deletes of marked branches and
  stopped protocols. The package also holds the `Variant` interface, the `Engine` type, and the
  published patterns.
- `data/datatest` is the conformance suite, `Run`, which an engine or a consumer's own variant
  runs against a live database.
- `postgres` (sub-module) is the PostgreSQL engine: `Engine`, whose variant takes an advisory
  tree lock, resolves a path in one statement, and holds a file without writing a row version,
  and `Migrations`, the schema's migration set.
- `example` (module) composes the library with PostgreSQL and `go-storage`'s Azure Blob provider
  through one adapter, and runs one file's whole life.

## Development

The repository is a Go workspace of three modules, `.`, `postgres`, and `example`. Tasks run
through [mise](https://mise.jdx.dev):

```sh
mise run check         # build, vet, format, fix, tidy, test, and lint every module, then sqlint; writes nothing
mise run currency      # report requirements, Go, tools, actions, and images behind their latest
mise run upgrade       # upgrade every module's go directive and requirements, and the tools, to their latest
mise run test          # the unit tier, every module, nothing installed
mise run lint          # golangci-lint over every module, then sqlint over the repository
mise run build         # each module built with the workspace off, against its own pins
mise run up            # the compose stack: PostgreSQL and Azurite
mise run integration   # the conformance suite and the engine's proofs against the stack
mise run example       # the composition proof against the stack
mise run acceptance    # up, integration, and example, then the stack reset
```

`compose.yml` runs PostgreSQL 18 on port 5434 and Azurite's blob service on port 10000, and
`mise.toml` sets the connection settings the integration tier and the example read. The
integration tier is not part of CI.

## License

[Apache License 2.0](LICENSE).
