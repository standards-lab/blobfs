// Package datatest is the conformance suite of the data package: the checks
// every data.Store must pass against a live database, whatever the engine,
// the returning commands' form, and the variant. An engine sub-module's
// integration tier runs it over the baseline and its own variant; a consumer
// with an Engine of its own runs it over that. It is a package of its own
// because another module's tests cannot import a _test.go helper.
//
// The package exports:
//
//   - [Run], which runs the suite against a live database
//   - [FileRows] and [DirectoryRows], which script blobfs's rows for the
//     query library's scripted driver, sqltest, so a consumer's unit tests
//     of code over the store read the rows its statements scan
//
// The suite imports no engine and no driver, and every statement it runs
// outside the store is standard SQL. Where an outcome belongs to a
// variation point or a returning command, it compares the store under test
// with a second store over the baseline on the same database, rows and
// refusals in text. It creates two tables of its own, standing in for a
// consumer's foreign keys into blobfs_file and blobfs_directory, and an
// index on blobfs_file (directory_id, created_at), which it drops again.
// See "datatest: the conformance suite" in docs/features.md.
package datatest
