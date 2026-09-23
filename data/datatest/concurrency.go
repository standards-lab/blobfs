package datatest

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// opposingMoves is the tree lock's proof: transaction A moves X under Y
// while transaction B moves Y under X, each through the full
// Directories.Move, interleaved through a wrapper of the variant under
// test that pauses each move once its lock has returned, with the commits
// held by the suite. A starts first and reaches the pause holding the
// lock; B then starts.
//
// When the store serializes, B blocks inside the lock while A holds it,
// through A's check and update and until A commits; B's lock then
// returns, and B's check sees X under Y and refuses with ErrCycle: no
// cycle exists. When the store does not serialize, B passes the no-op
// lock at once, A's check and update run and stay uncommitted, B's check
// then runs against the same committed state and passes, B's update
// waits on A's row locks and runs once A commits, both commit, and X and
// Y are each other's ancestor and unreachable from the root, which the
// suite asserts with a walk down from the root and a bounded walk up from
// each, and then repairs.
func (s *suite) opposingMoves(t *testing.T) {
	x := s.mkdir(t, "x-"+t.Name())
	y := s.mkdir(t, "y-"+t.Name())
	g, store := s.gatedStore(t)

	a := s.startMove(store, x.ID, y.ID, x.Version)
	releaseA := g.await(t, "A")
	b := s.startMove(store, y.ID, x.ID, y.Version)

	if s.store.Directories.Serializes() {
		g.wantNotArrived(t, "B")
		close(releaseA)
		if err := <-a.moved; err != nil {
			t.Fatalf("A's move: %v", err)
		}
		g.wantNotArrived(t, "B")
		a.commit <- struct{}{}
		if err := <-a.done; err != nil {
			t.Fatalf("A's commit: %v", err)
		}
		close(g.await(t, "B"))
		if err := <-b.moved; !errors.Is(err, blobfs.ErrCycle) {
			t.Fatalf("B's move = %v, want ErrCycle: its check ran after A's commit", err)
		}
		if err := <-b.done; err != nil {
			t.Fatalf("B's rollback: %v", err)
		}
		s.wantPath(t, x.ID, "/"+y.Name+"/moved")
		s.wantPath(t, y.ID, "/"+y.Name)
		if n := s.reachable(t, x.ID, y.ID); n != 2 {
			t.Errorf("%d of the two directories are reachable from the root, want both", n)
		}
		if s.ownAncestor(t, x.ID) || s.ownAncestor(t, y.ID) {
			t.Error("a directory is its own ancestor: a cycle formed under a serializing lock")
		}
		return
	}

	releaseB := g.await(t, "B")
	close(releaseA)
	if err := <-a.moved; err != nil {
		t.Fatalf("A's move on a variant that does not serialize: %v", err)
	}
	// A's update is uncommitted, so B's check sees X still under the root
	// and passes. B's update then waits on the row locks A's update took,
	// so B returns only once A commits; a B that returned ErrCycle would
	// mean its check ran after A's commit, which the no-op lock cannot
	// cause.
	close(releaseB)
	select {
	case err := <-b.moved:
		if err != nil {
			t.Fatalf("B's move = %v; its check ran before A committed, so nothing refused it", err)
		}
	case <-time.After(blocked):
	}
	a.commit <- struct{}{}
	if err := <-a.done; err != nil {
		t.Fatalf("A's commit: %v", err)
	}
	select {
	case err := <-b.moved:
		if err != nil {
			t.Fatalf("B's move after A's commit = %v; its check had passed already", err)
		}
	case <-time.After(unblocked):
		t.Fatal("B's move did not return after A committed")
	}
	b.commit <- struct{}{}
	if err := <-b.done; err != nil {
		t.Fatalf("B's commit: %v", err)
	}
	// The proof that a variant without the lock forms a cycle: both moves
	// committed, X is under Y and Y is under X, neither is reachable from
	// the root, and each is its own ancestor.
	xr, yr := s.directory(t, x.ID), s.directory(t, y.ID)
	if xr.ParentID == nil || yr.ParentID == nil || *xr.ParentID != y.ID || *yr.ParentID != x.ID {
		t.Fatalf("after both commits x's parent is %v and y's is %v, want each the other", xr.ParentID, yr.ParentID)
	}
	if n := s.reachable(t, x.ID, y.ID); n != 0 {
		t.Errorf("%d of the two directories are reachable from the root, want none: they form a cycle detached from the tree", n)
	}
	if !s.ownAncestor(t, x.ID) || !s.ownAncestor(t, y.ID) {
		t.Error("the two directories are not each their own ancestor; no cycle formed")
	}
	// Repair, so the rest of the database stays walkable: Y goes back
	// under the root, and X stays under Y.
	ph := s.db.Dialect().Placeholder
	s.exec(t, "UPDATE blobfs_directory SET parent_id = "+ph(1)+" WHERE id = "+ph(2), blobfs.RootID, y.ID)
	if n := s.reachable(t, x.ID, y.ID); n != 2 {
		t.Errorf("after the repair %d of the two directories are reachable, want both", n)
	}
}

// opposingSerializableMoves is the standard-tier alternative to the lock,
// checked on every variant: the same two opposing moves, each in a
// transaction the caller opened at serializable isolation, interleaved as
// on a variant without the lock (both past their locks, A's update
// uncommitted when B's check runs). The engine then refuses one of the
// two, at its update or at its commit, with sqlate.ErrSerializationFailure,
// and no cycle forms. On a serializing variant B's snapshot predates A's
// commit all the same, because the lock statement is B's first and takes
// the snapshot before it blocks, so B is refused at its update instead of
// at its check.
func (s *suite) opposingSerializableMoves(t *testing.T) {
	x := s.mkdir(t, "sx-"+t.Name())
	y := s.mkdir(t, "sy-"+t.Name())
	g, store := s.gatedStore(t)
	serializable := sqlate.Isolation(sql.LevelSerializable)

	a := s.startMove(store, x.ID, y.ID, x.Version, serializable)
	releaseA := g.await(t, "A")
	b := s.startMove(store, y.ID, x.ID, y.Version, serializable)
	var releaseB chan struct{}
	if !s.store.Directories.Serializes() {
		releaseB = g.await(t, "B")
	}
	close(releaseA)
	if err := <-a.moved; err != nil {
		t.Fatalf("A's move: %v", err)
	}
	if s.store.Directories.Serializes() {
		a.commit <- struct{}{}
		if err := <-a.done; err != nil {
			t.Fatalf("A's commit: %v", err)
		}
		releaseB = g.await(t, "B")
	}
	close(releaseB)
	if !s.store.Directories.Serializes() {
		// B's update waits on A's row locks; A commits meanwhile.
		time.Sleep(blocked / 2)
		a.commit <- struct{}{}
		if err := <-a.done; err != nil {
			t.Fatalf("A's commit: %v", err)
		}
	}
	var refused error
	select {
	case refused = <-b.moved:
	case <-time.After(unblocked):
		t.Fatal("B's move did not return")
	}
	if refused == nil {
		b.commit <- struct{}{}
		refused = <-b.done
	} else if err := <-b.done; err != nil {
		t.Fatalf("B's rollback: %v", err)
	}
	if !errors.Is(refused, sqlate.ErrSerializationFailure) {
		t.Errorf("B ended with %v, want a serialization failure: the engine must refuse the second of two opposing moves", refused)
	}
	if n := s.reachable(t, x.ID, y.ID); n != 2 {
		t.Errorf("%d of the two directories are reachable from the root, want both", n)
	}
	if s.ownAncestor(t, x.ID) || s.ownAncestor(t, y.ID) {
		t.Error("a cycle formed under serializable isolation")
	}
	s.wantPath(t, x.ID, "/"+y.Name+"/moved")
}

// gatedStore builds a store over a gated wrapper of the variant the
// engine under test builds, through an engine that wraps it, against the
// suite's catalog and dialect.
func (s *suite) gatedStore(t *testing.T) (*gated, *data.Store) {
	t.Helper()
	g := &gated{arrived: make(chan chan struct{})}
	gate := func(c *query.Catalog, d sqlate.Dialect, base *data.Standard) (data.Variant, error) {
		v, err := s.engine(c, d, base)
		if err != nil {
			return nil, err
		}
		g.Variant = v
		return g, nil
	}
	store, err := data.New(s.catalog, s.db.Dialect(), data.WithEngine(gate))
	if err != nil {
		t.Fatalf("data.New over the gated variant: %v", err)
	}
	return g, store
}

// gated wraps a variant so that every LockTree, once the wrapped lock has
// returned, sends a release channel of its own on arrived and waits on
// it before it returns. The suite drives two moves through it and decides
// when each proceeds past its lock, in arrival order; on a serializing
// variant a second lock call blocks inside the wrapped lock and never
// reaches arrived until the first transaction ends.
type gated struct {
	data.Variant
	arrived chan chan struct{}
}

// LockTree takes the wrapped variant's lock and then waits at the gate.
func (g *gated) LockTree(ctx context.Context, tx *sqlate.Tx) error {
	if err := g.Variant.LockTree(ctx, tx); err != nil {
		return err
	}
	release := make(chan struct{})
	g.arrived <- release
	<-release
	return nil
}

// await returns the release channel of the next move to reach the gate,
// failing the test when none arrives within the bound.
func (g *gated) await(t *testing.T, who string) chan struct{} {
	t.Helper()
	select {
	case release := <-g.arrived:
		return release
	case <-time.After(unblocked):
		t.Fatalf("%s never reached the pause after its lock", who)
		return nil
	}
}

// wantNotArrived fails the test when a move reaches the gate within the
// blocking bound: on a serializing variant it must be waiting inside the
// lock.
func (g *gated) wantNotArrived(t *testing.T, who string) {
	t.Helper()
	select {
	case <-g.arrived:
		t.Fatalf("%s passed the lock while the other transaction held it; the store reports it serializes", who)
	case <-time.After(blocked):
	}
}

// mover is one move under way on a goroutine: moved reports
// Directories.Move's result once it returns, commit tells the goroutine
// to commit, and done reports the commit, or the rollback that follows a
// refused move.
type mover struct {
	moved  chan error
	commit chan struct{}
	done   chan error
}

// startMove begins a transaction under opts on a goroutine, runs
// Directories.Move through store in it, renaming the directory to moved,
// and waits for the suite before committing.
func (s *suite) startMove(store *data.Store, id, parentID string, version int64, opts ...sqlate.TxOption) *mover {
	m := &mover{moved: make(chan error, 1), commit: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		tx, err := s.db.Begin(s.ctx, opts...)
		if err != nil {
			m.moved <- err
			m.done <- err
			return
		}
		_, err = store.Directories.Move(s.ctx, tx, id, parentID, "moved", version)
		m.moved <- err
		if err != nil {
			m.done <- tx.Rollback()
			return
		}
		<-m.commit
		m.done <- tx.Commit()
	}()
	return m
}

// reachable counts how many of the two directories a walk down from the
// root reaches. A directory in a cycle is never reached, because its
// chain of parents never arrives at the root, so the walk terminates
// whatever the two directories' state.
func (s *suite) reachable(t *testing.T, x, y string) int {
	t.Helper()
	p := s.db.Dialect().Placeholder
	text := "WITH RECURSIVE tree (id) AS (" +
		"SELECT d.id FROM blobfs_directory d WHERE d.parent_id IS NULL" +
		" UNION ALL SELECT d.id FROM blobfs_directory d JOIN tree t ON d.parent_id = t.id)" +
		" SELECT COUNT(*) FROM tree WHERE tree.id IN (" + p(1) + ", " + p(2) + ")"
	return s.count(t, s.db, text, x, y)
}

// ownAncestor reports whether the directory with id is met again on a
// walk up from itself, bounded to eight steps so the walk terminates on a
// cycle.
func (s *suite) ownAncestor(t *testing.T, id string) bool {
	t.Helper()
	p := s.db.Dialect().Placeholder
	text := "WITH RECURSIVE up (id, parent_id, depth) AS (" +
		"SELECT d.id, d.parent_id, CAST(0 AS integer) FROM blobfs_directory d WHERE d.id = " + p(1) +
		" UNION ALL SELECT d.id, d.parent_id, up.depth + 1 FROM blobfs_directory d JOIN up ON up.parent_id = d.id WHERE up.depth < 8)" +
		" SELECT COUNT(*) FROM up WHERE up.id = " + p(2)
	return s.count(t, s.db, text, id, id) > 1
}

// wantBlocked fails the test when done delivers within the blocking
// bound.
func wantBlocked(t *testing.T, done <-chan error, msg string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s (%v)", msg, err)
	case <-time.After(blocked):
	}
}

// awaitOrFail returns what done delivers, failing the test with msg when
// nothing arrives within the unblocking bound.
func awaitOrFail(t *testing.T, done <-chan error, msg string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(unblocked):
		t.Fatal(msg)
		return nil
	}
}
