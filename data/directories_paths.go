package data

import (
	"context"
	"fmt"
	"strings"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// FindByPath returns the directory at path below the directory with
// startID. A path is relative: a/b, directory names separated by slashes,
// each normalized before it is compared, and the empty path names the
// start itself. The library has no absolute path: a consumer whose own
// input syntax spells one from the root, /a/b, strips its leading slash and
// resolves from blobfs.RootID. So a path that starts with a slash is
// blobfs.ErrInvalidPath, as is one with an empty segment, a trailing
// slash, or a segment ValidateName refuses, which covers . and .. and so
// rules out upward navigation; a refused segment matches
// blobfs.ErrInvalidName as well. A start that is not a directory,
// including a file's id, is blobfs.ErrNotFound; a segment that names no
// directory is blobfs.ErrNotFound naming the prefix that failed.
//
// Resolution is the variant's: the baseline reads the start by id and then
// one child per segment, because standard SQL has no ordered array
// parameter to walk by in one statement, and an engine's variant may walk
// the whole path in one statement. The session may be the pool or a
// transaction. A consumer that holds a directory's id resolves below it
// without repeating the walk from the root.
func (d *Directories) FindByPath(ctx context.Context, sess sqlate.Session, startID, path string) (blobfs.Directory, error) {
	segments, err := splitPath(path)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: find %q from %s: %w", path, startID, err)
	}
	dir, depth, err := d.variant.ResolvePath(ctx, sess, startID, segments)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: find %q from %s: %w", path, startID, err)
	}
	if depth < len(segments) {
		return blobfs.Directory{}, fmt.Errorf("data: find %q from %s at %s: %w", path, startID, strings.Join(segments[:depth+1], "/"), blobfs.ErrNotFound)
	}
	return dir, nil
}

// Path returns the path of the directory with id from the root: / for the
// root and /a/b below it, the root's own name followed by the names of the
// chain from the root's child down to the directory, joined by slashes.
// The path is computed at read time, so a move changes it with no write
// below the moved directory. It is one recursive statement that walks
// upward from the directory, so its cost is the directory's depth. A
// directory that does not exist is blobfs.ErrNotFound. The path is for
// display; its text after the leading slash is the relative path
// FindByPath resolves from blobfs.RootID.
//
// A directory whose chain of parents loops, which two opposing concurrent
// moves on a variant whose Serializes reports false can leave, has no path:
// the walk stops once it returns to a directory it has visited, and Path
// reports blobfs.ErrCycle. A Move of a directory on the loop back under
// the root repairs the tree.
func (d *Directories) Path(ctx context.Context, sess sqlate.Session, id string) (string, error) {
	rows, err := d.ancestors.All(ctx, sess, query.Args{"id": id})
	if err != nil {
		return "", fmt.Errorf("data: path of %s: %w", id, err)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("data: path of %s: %w", id, blobfs.ErrNotFound)
	}
	chain, err := ancestry(rows)
	if err != nil {
		return "", fmt.Errorf("data: path of %s: %w", id, err)
	}
	var b strings.Builder
	for i := len(chain) - 2; i >= 0; i-- {
		b.WriteString("/")
		b.WriteString(chain[i].Name)
	}
	if b.Len() == 0 {
		return "/", nil
	}
	return b.String(), nil
}

// ancestry orders the rows directory_ancestors returned, in no set order,
// into the chain from the directory the walk started at up to the root.
// The start is the one row no other row names as its parent, found by the
// rows alone, so the id's spelling in the caller's argument does not
// matter; from it the chain follows parent_id and ends at the row without
// a parent. When every row is another's parent, or a parent is met twice,
// the tree loops: blobfs.ErrCycle. A parent the rows do not carry means
// the chain does not reach the root.
func ancestry(rows []ancestor) ([]ancestor, error) {
	byID := make(map[string]ancestor, len(rows))
	parents := make(map[string]bool, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
		if r.ParentID != nil {
			parents[*r.ParentID] = true
		}
	}
	start := ""
	for _, r := range rows {
		if !parents[r.ID] {
			start = r.ID
			break
		}
	}
	if start == "" {
		return nil, fmt.Errorf("every directory on the chain is another's parent, so the chain loops: %w", blobfs.ErrCycle)
	}
	chain := make([]ancestor, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for next := start; ; {
		r, ok := byID[next]
		if !ok {
			return nil, fmt.Errorf("the chain of %d ancestors does not reach the root", len(chain))
		}
		if seen[next] {
			return nil, fmt.Errorf("the chain of parents returns to %s and never reaches the root: %w", next, blobfs.ErrCycle)
		}
		seen[next] = true
		chain = append(chain, r)
		if r.ParentID == nil {
			return chain, nil
		}
		next = *r.ParentID
	}
}

// splitPath checks that path is relative and returns its normalized,
// validated segments; the empty path has none. A leading slash is refused,
// and a trailing slash is an empty segment and is refused. A segment
// validName refuses, including an empty one, is blobfs.ErrInvalidPath
// wrapping the name's error.
func splitPath(path string) ([]string, error) {
	if strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("%w: %q starts with /; a path is relative to the directory it starts from", blobfs.ErrInvalidPath, path)
	}
	if path == "" {
		return nil, nil
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		name, err := validName(segment)
		if err != nil {
			return nil, fmt.Errorf("%w: segment %d: %w", blobfs.ErrInvalidPath, i+1, err)
		}
		segments[i] = name
	}
	return segments, nil
}
