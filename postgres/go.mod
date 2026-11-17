module github.com/standards-lab/blobfs/postgres

go 1.27

require (
	github.com/jackc/pgx/v5 v5.13.0
	github.com/standards-lab/blobfs v0.8.0
	github.com/standards-lab/sqlate v0.7.0
	github.com/standards-lab/sqlate/postgres v0.7.0
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/standards-lab/sqlate/sqlint v0.4.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/text v0.44.0 // indirect
)

tool github.com/standards-lab/sqlate/sqlint/cmd/sqlint

// Temporary: pgtest, which the integration tier's databases come from, is
// unreleased. Until sqlate/postgres v0.8.0 is tagged the module resolves
// against the sibling sqlate checkout; at the tag the requirement moves to
// v0.8.0 and this replace goes.
replace github.com/standards-lab/sqlate/postgres => ../../sqlate/postgres
