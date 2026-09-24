package datatest

import (
	"errors"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// moves checks the tree lock and the directory move.
func (s *suite) moves(t *testing.T) {
	t.Run("LockTree", s.lockTree)
	t.Run("RootRefused", s.moveRoot)
	t.Run("IntoOwnSubtreeRefused", s.moveIntoOwnSubtree)
	t.Run("MovesTheSubtree", s.moveSubtree)
	t.Run("Renames", s.renameDirectory)
	t.Run("Refusals", s.moveDirectoryRefusals)
	t.Run("OpposingConcurrentMoves", s.opposingMoves)
	t.Run("OpposingSerializableMoves", s.opposingSerializableMoves)
}

// lockTree checks the tree lock against what Serializes reports: when the
// store serializes, a second transaction's LockTree blocks while the
// first holds the lock and returns once the first commits or rolls back;
// when it does not, the lock is a no-op that never blocks.
func (s *suite) lockTree(t *testing.T) {
	if s.store.Directories.Serializes() != s.variant.Serializes() {
		t.Errorf("the store reports Serializes %v and its variant %v", s.store.Directories.Serializes(), s.variant.Serializes())
	}
	if s.baseline.Directories.Serializes() {
		t.Error("the baseline reports it serializes; standard SQL has no lock to take")
	}
	if s.store.Directories.Serializes() {
		t.Run("HeldToCommit", func(t *testing.T) { s.heldUntil(t, (*sqlate.Tx).Commit) })
		t.Run("HeldToRollback", func(t *testing.T) { s.heldUntil(t, (*sqlate.Tx).Rollback) })
		return
	}
	t.Run("NoOpNeverBlocks", func(t *testing.T) {
		first := s.beginTx(t)
		defer func() { _ = first.Rollback() }()
		if err := s.store.Directories.LockTree(s.ctx, first); err != nil {
			t.Fatalf("the first LockTree: %v", err)
		}
		second := s.beginTx(t)
		defer func() { _ = second.Rollback() }()
		if err := awaitOrFail(t, s.lockIn(second), "the second LockTree blocked while the first transaction held the lock; a variant that does not serialize must not block"); err != nil {
			t.Fatalf("the second LockTree: %v", err)
		}
	})
}

// heldUntil proves a second transaction's LockTree blocks while the first
// holds the lock and returns once the first ends through end.
func (s *suite) heldUntil(t *testing.T, end func(*sqlate.Tx) error) {
	first := s.beginTx(t)
	ended := false
	defer func() {
		if !ended {
			_ = first.Rollback()
		}
	}()
	if err := s.store.Directories.LockTree(s.ctx, first); err != nil {
		t.Fatalf("the first LockTree: %v", err)
	}
	second := s.beginTx(t)
	defer func() { _ = second.Rollback() }()
	done := s.lockIn(second)
	wantBlocked(t, done, "the second LockTree returned while the first transaction held the lock")
	ended = true
	if err := end(first); err != nil {
		t.Fatalf("ending the first transaction: %v", err)
	}
	if err := awaitOrFail(t, done, "the second LockTree still blocks after the first transaction ended"); err != nil {
		t.Fatalf("the second LockTree after the first ended: %v", err)
	}
}

// lockIn takes the tree lock in tx on a goroutine and reports the result
// on the channel.
func (s *suite) lockIn(tx *sqlate.Tx) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.store.Directories.LockTree(s.ctx, tx) }()
	return done
}

// moveRoot checks the root is refused before any SQL and stays as it was.
func (s *suite) moveRoot(t *testing.T) {
	root := s.directory(t, blobfs.RootID)
	dir := s.mkdir(t, "root-target-"+t.Name())
	_, err := s.move(t, blobfs.RootID, dir.ID, "root", root.Version)
	if !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("Move(root) = %v, want ErrRootDirectory", err)
	}
	if after := s.directory(t, blobfs.RootID); !equalDirectory(after, root) {
		t.Errorf("the root changed to %+v from %+v", after, root)
	}
}

// moveIntoOwnSubtree checks the cycle check: a move of a directory under
// itself or under any descendant is ErrCycle, the same text on both
// stores, and nothing changes; IsWithin answers the same question.
func (s *suite) moveIntoOwnSubtree(t *testing.T) {
	a := s.mkdir(t, "cycle-"+t.Name())
	b := s.mkdirUnder(t, a.ID, "b")
	c := s.mkdirUnder(t, b.ID, "c")
	for _, target := range []blobfs.Directory{a, b, c} {
		_, err := s.move(t, a.ID, target.ID, "a", a.Version)
		if !errors.Is(err, blobfs.ErrCycle) {
			t.Errorf("Move of a under %s = %v, want ErrCycle", target.Name, err)
		}
		_, base := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
			return s.baseline.Directories.Move(s.ctx, tx, a.ID, target.ID, "a", a.Version)
		})
		wantSameError(t, err, base)
		if within, err := s.store.Directories.IsWithin(s.ctx, s.db, target.ID, a.ID); err != nil || !within {
			t.Errorf("IsWithin(%s, a) = %v, %v, want true", target.Name, within, err)
		}
	}
	if within, err := s.store.Directories.IsWithin(s.ctx, s.db, a.ID, b.ID); err != nil || within {
		t.Errorf("IsWithin(a, b) = %v, %v, want false: a is b's ancestor, not its descendant", within, err)
	}
	if after := s.directory(t, a.ID); !equalDirectory(after, a) {
		t.Errorf("the refused moves changed the row to %+v from %+v", after, a)
	}
}

// moveSubtree checks a move against the baseline: the moved row under its
// new parent at the next version, the row the database holds, its
// descendants and their files following it by id and unchanged, the paths
// computed anew, and the old path no longer resolving.
func (s *suite) moveSubtree(t *testing.T) {
	src := s.mkdir(t, "src-"+t.Name())
	dst := s.mkdir(t, "dst-"+t.Name())
	x := s.mkdirUnder(t, src.ID, "x")
	y := s.mkdirUnder(t, x.ID, "y")
	fx := s.insertFile(t, x.ID, "in-x.txt", blobfs.StatusAvailable)
	fy := s.insertFile(t, y.ID, "in-y.txt", blobfs.StatusAvailable)
	fileBefore := s.file(t, fx)
	moved, err := s.move(t, x.ID, dst.ID, "x", x.Version)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.ParentID == nil || *moved.ParentID != dst.ID || moved.Name != "x" || moved.Version != x.Version+1 || !moved.UpdatedAt.After(x.UpdatedAt) || !moved.CreatedAt.Equal(x.CreatedAt) {
		t.Errorf("the moved row is %+v, want it under dst at the next version", moved)
	}
	if after := s.directory(t, x.ID); !equalDirectory(after, moved) {
		t.Errorf("Move returned %+v but the database holds %+v", moved, after)
	}
	s.wantPath(t, x.ID, "/"+dst.Name+"/x")
	s.wantPath(t, y.ID, "/"+dst.Name+"/x/y")
	if after := s.directory(t, y.ID); !equalDirectory(after, y) {
		t.Errorf("the child changed to %+v from %+v; it follows its parent by id", after, y)
	}
	if after := s.file(t, fx); !equalFile(after, fileBefore) {
		t.Errorf("the file changed to %+v from %+v; it follows its directory by id", after, fileBefore)
	}
	if f := s.file(t, fy); f.DirectoryID != y.ID {
		t.Errorf("the deep file is in %s, want %s", f.DirectoryID, y.ID)
	}
	if _, err := s.store.Directories.FindByPath(s.ctx, s.db, blobfs.RootID, src.Name+"/x"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("the old path still resolves: %v", err)
	}
	baseDir := s.mkdirUnder(t, src.ID, "base")
	base, err := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
		return s.baseline.Directories.Move(s.ctx, tx, baseDir.ID, dst.ID, "base", baseDir.Version)
	})
	if err != nil || *base.ParentID != dst.ID || base.Version != moved.Version {
		t.Errorf("the baseline's Move = %+v, %v, want the same shape as %+v", base, err, moved)
	}
}

// renameDirectory checks a move under the same parent as a rename: the
// name stored composed, the version advanced, and a child's path
// following.
func (s *suite) renameDirectory(t *testing.T) {
	d := s.mkdir(t, "rename-"+t.Name())
	child := s.mkdirUnder(t, d.ID, "child")
	n := name("renamed-" + t.Name() + "-" + decomposed)
	renamed, err := s.move(t, d.ID, blobfs.RootID, n, d.Version)
	if err != nil {
		t.Fatalf("Move as a rename: %v", err)
	}
	if *renamed.ParentID != blobfs.RootID || renamed.Name != blobfs.NormalizeName(n) || renamed.Version != d.Version+1 {
		t.Errorf("the renamed row is %+v", renamed)
	}
	s.wantPath(t, child.ID, "/"+renamed.Name+"/child")
}

// moveDirectoryRefusals checks the move's refusals against the baseline,
// each leaving the row unchanged: a name held under the new parent is
// ErrNameTaken under the unique constraint, while a file of that name is
// no conflict; a missing parent is ErrNotFound under the foreign key; a
// missing directory is ErrNotFound; a stale version is
// ErrVersionMismatch; and a refused name is a NameError.
func (s *suite) moveDirectoryRefusals(t *testing.T) {
	p := s.mkdir(t, "taken-"+t.Name())
	s.mkdirUnder(t, p.ID, "held")
	s.insertFile(t, p.ID, "file-name", blobfs.StatusAvailable)
	d := s.mkdir(t, "mover-"+t.Name())
	missing := blobfs.NewID()
	for _, c := range []struct {
		name       string
		id         string
		parent     string
		dir        string
		version    int64
		want       error
		constraint string
	}{
		{"NameTaken", d.ID, p.ID, "held", d.Version, blobfs.ErrNameTaken, blobfs.ConstraintUniqueDirectoryParentName},
		{"MissingParent", d.ID, blobfs.NewID(), "d", d.Version, blobfs.ErrNotFound, blobfs.ConstraintForeignKeyDirectoryParent},
		{"MissingDirectory", missing, blobfs.RootID, "ghost", 1, blobfs.ErrNotFound, ""},
		{"StaleVersion", d.ID, blobfs.RootID, name("stale-" + t.Name()), d.Version + 1, query.ErrVersionMismatch, ""},
		{"RefusedName", d.ID, blobfs.RootID, "a/b", d.Version, blobfs.ErrInvalidName, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.move(t, c.id, c.parent, c.dir, c.version)
			if c.constraint != "" {
				wantViolation(t, err, c.want, c.constraint)
			} else if !errors.Is(err, c.want) {
				t.Errorf("Move = %v, want %v", err, c.want)
			}
			_, base := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
				return s.baseline.Directories.Move(s.ctx, tx, c.id, c.parent, c.dir, c.version)
			})
			wantSameError(t, err, base)
			if after := s.directory(t, d.ID); !equalDirectory(after, d) {
				t.Errorf("the refused move changed the row to %+v from %+v", after, d)
			}
		})
	}
	if _, err := s.move(t, d.ID, p.ID, "file-name", d.Version); err != nil {
		t.Errorf("Move under the name of a file = %v, want the move to succeed: the name spaces are separate", err)
	}
}

// move runs Directories.Move through the store under test in a
// transaction of its own and commits it.
func (s *suite) move(t *testing.T, id, parentID, n string, version int64) (blobfs.Directory, error) {
	t.Helper()
	return s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
		return s.store.Directories.Move(s.ctx, tx, id, parentID, n, version)
	})
}
