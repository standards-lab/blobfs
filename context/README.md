# blobfs

blobfs maps flat blob objects, which a consumer stores under opaque keys, onto a virtual tree of
directories and file-metadata rows in SQL, one directory at a time. It never calls the object
store: it exposes the steps of a two-phase write and delete, and the consumer sequences them.

The user guide is the repository's own: `README.md` is the index and `docs/` the documents it
orders. The library is adjacent to the organization's Go Elemental standard, and the standard's
[catalog](https://github.com/standards-lab/architecture/blob/main/standards/go-elemental/README.md)
names it as such. This context records only working knowledge the guide and the code do not
express.

## Capability map

The first release is built, and `docs/` describes it: the root package, the persistence
package `data` with its conformance suite `data/datatest`, the `postgres` engine sub-module
(its variant, native statements, migration set, and integration tier), and the `example`
module, the composition proof with `go-storage`. Its provenance is the closed experiment
[spike-blobfs](https://github.com/JaimeStill/spike-blobfs).

## Notes

None yet.
