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

// Patterns is blobfs's published pattern source, the column lists
// directory_columns and file_columns, which a consumer registers in its
// catalog beside query.Patterns(). Every pattern is parameter-free, since
// a projection base that included one would gain its parameters.
func Patterns() query.Source {
	return query.Publish(Namespace, patternFiles, "patterns")
}
