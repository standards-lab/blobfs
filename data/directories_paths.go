package data

import (
	"context"
	"fmt"
	"strings"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// FindByPath returns the directory at the relative path a/b below startID;
// the empty path is the start. Refusals: blobfs.ErrInvalidPath, and
// blobfs.ErrNotFound with the failing prefix in the text. See Directories
// in docs/features.md.
func (d *Directories) FindByPath(ctx context.Context, sess sqlate.Session, startID, path string) (_ blobfs.Directory, err error) {
	defer wrap(&err, "find %q from %s", path, startID)
	segments, err := splitPath(path)
	if err != nil {
		return blobfs.Directory{}, err
	}
	dir, depth, err := d.variant.ResolvePath(ctx, sess, startID, segments)
	switch {
	case err != nil:
		return blobfs.Directory{}, err
	case depth < len(segments):
		return blobfs.Directory{}, fmt.Errorf("at %s: %w", strings.Join(segments[:depth+1], "/"), blobfs.ErrNotFound)
	}
	return dir, nil
}

// Path returns the path of the directory with id from the root, / for the
// root and /a/b below it, computed at read time in one statement whose
// cost is the directory's depth. Its text after the leading slash is the
// path FindByPath resolves from blobfs.RootID.
//
// Refusals: blobfs.ErrNotFound; blobfs.ErrCycle when the chain of parents
// loops.
func (d *Directories) Path(ctx context.Context, sess sqlate.Session, id string) (_ string, err error) {
	defer wrap(&err, "path of %s", id)
	rows, err := d.ancestors.All(ctx, sess, query.Args{"id": id})
	switch {
	case err != nil:
		return "", err
	case len(rows) == 0:
		return "", blobfs.ErrNotFound
	}
	chain, err := ancestry(rows)
	if err != nil {
		return "", err
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

// ancestry orders the rows of directory_ancestors into the chain from the
// start up to the root. The start is the row no other row names as its
// parent, found by the rows alone so the id's spelling does not matter; a
// loop is blobfs.ErrCycle.
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

// splitPath returns the normalized, validated segments of a relative
// path, or blobfs.ErrInvalidPath wrapping the refusal.
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
