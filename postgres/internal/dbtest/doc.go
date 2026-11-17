//go:build integration

// Package dbtest is the support the integration tier's tests share beyond
// their databases, which come from sqlate/postgres/pgtest: each test
// creates its own throwaway database there on the server BLOBFS_DSN names,
// so no test depends on the state of the compose stack's database or on
// another test, and pgtest drops it when the test ends. A missing
// BLOBFS_DSN fails the test rather than skipping it: the integration tag
// states that the stack is expected. This package names that variable and
// the databases' prefix, loads blobfs's migration set, explains statements
// for the plan-shape assertions, and seeds the cost fixtures.
//
// The package exports:
//
//   - [Env] and [Prefix], which a test passes to pgtest.Create or
//     pgtest.Migrated, and [Migrations], blobfs's migration set
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
