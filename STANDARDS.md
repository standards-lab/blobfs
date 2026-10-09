# blobfs standards

The judgement calls the standards-reviewer applies to blobfs, beyond what `mise run check` enforces.

- A change that alters documented behavior updates the guide, `README.md` and `docs/`, in the same change.
- `architecture/standards/go-elemental/principles/dependencies.md`: the bottom-up line and no provider in a base, across the base, `postgres` and `example` `go.mod` files.
- `architecture/standards/go-elemental/principles/tests-and-docs.md`: the doc.go inventory of every package, the unit tier over `sqltest`, and the `postgres` integration tier that runs `data/datatest`.
- `architecture/standards/go-elemental/principles/topology-and-naming.md`: the base module, the `postgres` engine sub-module and its tags, and `example` as a module of its own.
- `architecture/standards/go-elemental/principles/release-and-ci.md`: the check, currency, the root `go.work`, and a changelog per module.
- `architecture/standards/go-elemental/principles/dsl-driven-services.md`: every authored `.sql` file, in `data/statements`, `data/patterns`, `postgres/statements` and `postgres/migrations`.
- `architecture/standards/go-elemental/principles/baseline-standards.md`: `data`'s listings take their page from the caller, and the sweep's stale age has no default.
- `architecture/standards/go-elemental/principles/utc-times.md`: `File`'s and `Directory`'s `CreatedAt` and `UpdatedAt`, read through sqlate's `query.Scanner`.
- `architecture/principles/service-tiers.md`: `data`'s standard-tier statements, and the `Variant` an engine sub-module's `Engine` supplies its native forms through.
- `architecture/principles/context-architecture.md`: the guide and each `doc.go` are the homes.
