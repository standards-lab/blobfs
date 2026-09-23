package data

import (
	"embed"

	"github.com/standards-lab/sqlate/query"
)

//go:embed patterns/*.sql
var patternFiles embed.FS

// Namespace is the pattern namespace blobfs publishes: a consumer's
// statement includes one of its patterns as {{> blobfs.name}}.
const Namespace = "blobfs"

// Patterns is blobfs's published pattern source: the column lists of the
// directory and file rows, directory_columns and file_columns,
// standard-tier fragments a consumer's statements include. The consumer
// registers it in its one catalog beside query.Patterns() and its own
// sources. Every pattern is parameter-free and includes no other pattern:
// a pattern cannot include one, and a pattern's slots would become
// parameters of the including statement, which a projection base rejects.
func Patterns() query.Source {
	return query.Publish(Namespace, patternFiles, "patterns")
}
