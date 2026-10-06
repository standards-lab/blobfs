//go:build integration

// Package dbtest is the support the integration tier's tests share: it
// gives each test its own throwaway database on the server BLOBFS_DSN
// names, so no test depends on the state of the compose stack's database
// or on another test, and drops the database when the test ends. It
// applies blobfs's migration set through the query library's migrator,
// explains statements for the plan-shape assertions, and seeds the cost
// fixtures. A missing BLOBFS_DSN fails the test rather than skipping it:
// the integration tag states that the stack is expected.
//
// The package exports:
//
//   - [Database], one test's throwaway database, and [Create] and
//     [Migrated], which make it empty or with blobfs's migration set applied
//   - [Constraint], which extracts the constraint name a classified
//     violation carries
//   - [Exists] and [Int], one-row catalog and value queries for assertions
//   - [Explainer] and [NewExplainer], which run statements under EXPLAIN
//     for the cost assertions, and [Plan], one explained statement
//   - [Sizes], [Tree] and [SeedTree], the cost fixture's shape, its seeded
//     result, and the seeding
//   - [HeapBlocks] and [RelationPages], page counts the cost assertions
//     compare plans against
package dbtest
