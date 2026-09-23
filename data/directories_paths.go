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
func (d *Directories) Path(ctx context.Context, sess sqlate.Session, id string) (string, error) {
	chain, err := d.ancestors.All(ctx, sess, query.Args{"id": id})
	if err != nil {
		return "", fmt.Errorf("data: path of %s: %w", id, err)
	}
	if len(chain) == 0 {
		return "", fmt.Errorf("data: path of %s: %w", id, blobfs.ErrNotFound)
	}
	if chain[0].ParentID != nil {
		return "", fmt.Errorf("data: path of %s: the chain of %d ancestors does not reach the root", id, len(chain))
	}
	var b strings.Builder
	for _, a := range chain[1:] {
		b.WriteString("/")
		b.WriteString(a.Name)
	}
	if b.Len() == 0 {
		return "/", nil
	}
	return b.String(), nil
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
