//go:build integration

package dbtest

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Explainer runs EXPLAIN (ANALYZE, BUFFERS) for the cost assertions. It
// holds a pool of its own over pgx's simple protocol, so every run is
// planned with its literal values, as a custom plan is, and no
// prepared-statement cache switches to a generic plan after a few runs.
// Each statement runs inside a transaction that is rolled back, so an
// explained write leaves the fixture as it found it.
type Explainer struct {
	pool *sql.DB
}

// NewExplainer opens the pool on the database dsn names. The pool is
// closed when the test ends.
func NewExplainer(t testing.TB, dsn string) *Explainer {
	t.Helper()
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pool, err := sql.Open("pgx", dsn+sep+"default_query_exec_mode=simple_protocol")
	if err != nil {
		t.Fatalf("open the explain pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return &Explainer{pool: pool}
}

// Plan is one explained statement: the plan text as EXPLAIN printed it
// and the top node's shared buffers, hit and read added, in 8 KB pages.
type Plan struct {
	Text    string
	Buffers int
}

// buffersRe matches the first Buffers line, which belongs to the top
// node; the planner's own buffers come after the tree, under Planning.
var buffersRe = regexp.MustCompile(`Buffers: shared( hit=(\d+))?( read=(\d+))?`)

// Explain runs the statement under EXPLAIN (ANALYZE, BUFFERS) twice, each
// time in a transaction that is rolled back, and returns the second run:
// the first warms the cache and sets the hint bits of the rows it
// touches, so the buffer count is the statement's own.
func (e *Explainer) Explain(ctx context.Context, t testing.TB, query string, args ...any) Plan {
	t.Helper()
	var text string
	for range 2 {
		text = e.once(ctx, t, query, args...)
	}
	p := Plan{Text: text}
	if m := buffersRe.FindStringSubmatch(text); m != nil {
		p.Buffers = atoi(m[2]) + atoi(m[4])
	}
	return p
}

// once runs one explained execution in a rolled-back transaction and
// returns the plan text.
func (e *Explainer) once(ctx context.Context, t testing.TB, query string, args ...any) string {
	t.Helper()
	tx, err := e.pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, query)
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("explain: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain: %v", err)
	}
	return strings.Join(lines, "\n")
}

// Has reports whether the plan text contains s: a node name such as
// "Seq Scan on blobfs_file" or "WindowAgg".
func (p Plan) Has(s string) bool {
	return strings.Contains(p.Text, s)
}

// Lines returns the plan's lines that contain s, each trimmed, so a test
// can check what an "Index Cond:" or a "Filter:" line holds.
func (p Plan) Lines(s string) []string {
	var out []string
	for _, line := range strings.Split(p.Text, "\n") {
		if strings.Contains(line, s) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// heapScanRe matches a plan node that reads a table's rows: a sequential
// scan, an index scan in either direction, an index-only scan, or a
// bitmap heap scan. A bitmap index scan feeds a bitmap heap scan and is
// not a second read of the table.
var heapScanRe = regexp.MustCompile(`(Seq Scan|Index Scan|Index Scan Backward|Index Only Scan|Index Only Scan Backward|Bitmap Heap Scan)( using \S+)? on (\S+)`)

// HeapScans counts the plan nodes that read table's rows, so a test can
// require that a statement reads a table once.
func (p Plan) HeapScans(table string) int {
	n := 0
	for _, m := range heapScanRe.FindAllStringSubmatch(p.Text, -1) {
		if m[3] == table {
			n++
		}
	}
	return n
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
