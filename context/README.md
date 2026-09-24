# blobfs

blobfs maps flat blob objects, which a consumer stores under opaque keys, onto a tree of
directories and file-metadata rows in SQL, one directory at a time. It never calls the object store: it exposes the steps of the
two-phase write and the two-phase delete, and the consumer runs its store's calls between them.

The user guide is the repository's own: `README.md` is the index, and `docs/` holds the
documents it lists in reading order. The library is adjacent to the organization's Go Elemental
standard, and the standard's
[catalog](https://github.com/standards-lab/architecture/blob/main/standards/go-elemental/README.md)
names it as such. This context records only working knowledge the guide and the code do not
express.

## Capability map

The repository holds the first release, which `docs/` describes: the root package, the
persistence package `data` with its conformance suite `data/datatest`, the `postgres` engine
sub-module (its variant, native statements, migration set, and integration tier), and the
`example` module, which composes the library with `go-storage`.

## Notes

- `deferred.md`: what the library leaves out on purpose, each with its trigger, and the
  assumptions its design rests on.
