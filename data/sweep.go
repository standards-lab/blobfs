package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// ObjectDeleter is the consumer's object store as Store.Sweep calls it:
// the delete of the object under a file's key, the one step of a sweep
// outside the database. The consumer adapts its own store to it, so the
// data package names no object store.
//
// DeleteObject must be idempotent: an object that does not exist is
// success, not an error. A pass that stopped after an object's delete and
// before its row's purge deletes the object again, and the object of a
// pending row may never have been stored. Any error stops the pass and
// leaves the file's row deleting, for the next pass to finish.
type ObjectDeleter interface {
	DeleteObject(ctx context.Context, key string) error
}

// SweepResult is what one pass of Store.Sweep did: the files of branches
// being deleted whose objects it deleted and whose rows it purged, the
// directories it removed, and the pending rows it reclaimed. More reports
// that the pass stopped at its Batch bound, or at a row that reached a
// branch after the branch's mark, with work remaining; a caller runs
// passes while More is true.
type SweepResult struct {
	Files       int
	Directories int
	Pending     int
	More        bool
}

// Sweep runs one bounded pass that finishes the deletes a caller began and
// did not complete: the branches Directories.MarkDeleting marked and, with
// PendingOlderThan, the pending rows of abandoned writes. The pass is
// stateless: it finds its work in the database each time, and every step
// it takes is idempotent, so a pass stopped at any point, by an error or a
// crash, is finished by the next one. It calls objects for every object it
// deletes and never otherwise touches the object store.
//
// For each branch root Directories.Deleting returns, in id order, the pass
// first marks the branch again, in a transaction of its own, which reaches
// any straggler a create that raced the first mark left active in it. It
// then walks the branch depth first through the listings with
// IncludeDeleting. In each directory it deletes every file's object and
// then purges the file's row, as the file delete's last two steps do; then
// it empties and removes each child directory the same way; then it
// removes the directory itself, leaf directories before their parents and
// the root last. Each directory is removed in a transaction of its own,
// guarded by the version the pass read it at, with OnRemoveDirectory's
// function run first in the same transaction. A row still active in the
// branch when the pass reaches it, one that landed after this pass's mark,
// or a directory refused as blobfs.ErrNotEmpty for the same reason, stops
// the branch's walk with More set, and the next pass marks it. With
// PendingOlderThan, the budget the branches leave reclaims the oldest
// pending rows (see PendingOlderThan).
//
// Batch bounds the records the pass handles. When the pass spends the
// bound, it reads whether any work remains, a branch being deleted or,
// with PendingOlderThan, a pending row past its age, and reports that as
// More. The pass handles no file row that is deleting outside a branch
// being deleted, a single file whose delete stopped after Files.Delete:
// its delete is its caller's to finish.
//
// It takes the *sqlate.DB and not a session: it reads and purges on the
// pool, deletes objects outside any transaction, so no row lock is held
// across a call to the object store, and opens one transaction for each
// mark, each directory's removal, and each pending row's delete, which a
// *sqlate.Tx cannot. A refusal stops the branch or the pending row it
// meets, not the pass: an object delete's error, a hook's, and a purge or
// removal a consumer's foreign key refuses, as blobfs.ErrReferenced. The
// pass goes on to the next branch or row, so a row refused on every pass
// does not hold back the work behind it, and returns every refusal joined
// and wrapped as "data: sweep: ...", with the result counting what it did. A Batch below 1
// and a PendingOlderThan age that is not positive are refused before any
// SQL. Nothing to do is a zero result and no error.
func (s *Store) Sweep(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, opts ...SweepOption) (SweepResult, error) {
	o := sweepOptions{batch: defaultBatch}
	for _, opt := range opts {
		opt(&o)
	}
	switch {
	case o.batch < 1:
		return SweepResult{}, fmt.Errorf("data: sweep: the batch %d is below 1", o.batch)
	case o.hasPending && o.pendingAge <= 0:
		return SweepResult{}, fmt.Errorf("data: sweep: the pending age %s is not positive", o.pendingAge)
	}
	w := &sweep{store: s, db: db, objects: objects, opts: o, budget: o.batch}
	if o.hasPending {
		w.before = time.Now().Add(-o.pendingAge)
	}
	if err := w.run(ctx); err != nil {
		return w.result, fmt.Errorf("data: sweep: %w", err)
	}
	return w.result, nil
}

// sweep is one pass's state: what it has done, and the budget of records
// Batch left it.
type sweep struct {
	store   *Store
	db      *sqlate.DB
	objects ObjectDeleter
	opts    sweepOptions
	// before is the instant a pending row must have been last written
	// before to be reclaimed, set only with PendingOlderThan.
	before time.Time
	budget int
	result SweepResult
}

// run is the pass: the branches, then the pending rows with what budget
// is left, then, when the budget is spent, the read of whether work
// remains.
func (w *sweep) run(ctx context.Context) error {
	roots, err := w.store.Directories.Deleting(ctx, w.db, w.budget)
	if err != nil {
		return err
	}
	var errs []error
	for _, root := range roots {
		if w.budget == 0 {
			break
		}
		if err := w.branch(ctx, root); err != nil {
			errs = append(errs, fmt.Errorf("branch %s: %w", root.ID, err))
		}
	}
	if w.opts.hasPending && w.budget > 0 {
		errs = append(errs, w.pending(ctx)...)
	}
	if w.budget == 0 && !w.result.More {
		more, err := w.remains(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		w.result.More = more
	}
	return errors.Join(errs...)
}

// branch finishes the delete of the branch under root, as far as the
// budget allows: the mark repeated, then the walk from the root.
func (w *sweep) branch(ctx context.Context, root blobfs.Directory) error {
	_, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (Marked, error) {
		return w.store.Directories.MarkDeleting(ctx, tx, root.ID)
	})
	switch {
	case errors.Is(err, blobfs.ErrNotFound):
		// A concurrent pass removed the root since the roots were read.
		return nil
	case err != nil:
		return err
	}
	_, err = w.directory(ctx, root)
	return err
}

// directory empties the deleting directory dir and removes it: its files,
// then its child directories, each emptied and removed the same way, then
// dir itself. It reports whether dir is gone. It stops, reporting false,
// when the budget runs out, and when it meets a row of the branch that is
// not deleting, which a later mark reaches, with More set.
func (w *sweep) directory(ctx context.Context, dir blobfs.Directory) (bool, error) {
	for {
		if w.budget == 0 {
			return false, nil
		}
		page, err := w.store.Files.List(ctx, w.db, dir.ID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: w.budget}, IncludeDeleting())
		if err != nil {
			return false, err
		}
		for _, f := range page.Items {
			if f.Status != blobfs.StatusDeleting {
				w.result.More = true
				return false, nil
			}
			if err := w.finish(ctx, f); err != nil {
				return false, err
			}
			w.result.Files++
		}
		if !page.More {
			break
		}
	}
	for {
		if w.budget == 0 {
			return false, nil
		}
		page, err := w.store.Directories.List(ctx, w.db, dir.ID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: w.budget}, IncludeDeleting())
		if err != nil {
			return false, err
		}
		if len(page.Items) == 0 {
			break
		}
		for _, child := range page.Items {
			if child.Status != blobfs.DirectoryStatusDeleting {
				w.result.More = true
				return false, nil
			}
			if gone, err := w.directory(ctx, child); err != nil || !gone {
				return false, err
			}
		}
	}
	if w.budget == 0 {
		return false, nil
	}
	return w.remove(ctx, dir)
}

// remove removes the emptied directory dir in a transaction of its own,
// after the consumer's hook in the same transaction, guarded by the
// version the pass read dir at, which a deleting directory keeps. A
// directory already gone was removed by a concurrent pass. One a straggler
// landed in since the walk emptied it is left, with More set.
func (w *sweep) remove(ctx context.Context, dir blobfs.Directory) (bool, error) {
	var hookErr error
	_, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (struct{}, error) {
		if w.opts.onRemove != nil {
			if hookErr = w.opts.onRemove(ctx, tx, dir); hookErr != nil {
				return struct{}{}, hookErr
			}
		}
		return struct{}{}, w.store.Directories.Delete(ctx, tx, dir.ID, AtVersion(dir.Version))
	})
	switch {
	case hookErr != nil:
		return false, fmt.Errorf("remove directory %s: the hook: %w", dir.ID, err)
	case errors.Is(err, blobfs.ErrNotFound):
		return true, nil
	case errors.Is(err, blobfs.ErrNotEmpty):
		w.result.More = true
		return false, nil
	case err != nil:
		return false, err
	}
	w.result.Directories++
	w.budget--
	return true, nil
}

// finish runs the last two steps of the delete of the deleting file f:
// the delete of its object, then the purge of its row, and spends one
// record of the budget.
func (w *sweep) finish(ctx context.Context, f blobfs.File) error {
	if err := w.objects.DeleteObject(ctx, f.Key); err != nil {
		return fmt.Errorf("delete the object of file %s: %w", f.ID, err)
	}
	if err := w.store.Files.Purge(ctx, w.db, f.ID); err != nil {
		return err
	}
	w.budget--
	return nil
}

// pending reclaims the oldest pending rows last written before w.before,
// as many as the budget allows: each is moved to deleting in a
// transaction of its own at the version the pass read, then finished. A
// row that moved on since the read, completed or gone, is skipped and
// spends nothing. A row refused is skipped too, its refusal returned with
// the others'.
func (w *sweep) pending(ctx context.Context) []error {
	rows, err := w.store.Files.pending.All(ctx, w.db, query.Args{"before": w.before, "offset": 0, "fetch": w.budget})
	if err != nil {
		return []error{fmt.Errorf("read the pending files: %w", err)}
	}
	var errs []error
	for _, f := range rows {
		file, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
			return w.store.Files.Delete(ctx, tx, f.ID, AtVersion(f.Version))
		})
		switch {
		case errors.Is(err, blobfs.ErrNotFound), errors.Is(err, query.ErrVersionMismatch):
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("pending file %s: %w", f.ID, err))
			continue
		}
		if err := w.finish(ctx, file); err != nil {
			errs = append(errs, fmt.Errorf("pending file %s: %w", f.ID, err))
			continue
		}
		w.result.Pending++
	}
	return errs
}

// remains reports whether work is left for another pass: a branch being
// deleted or, with PendingOlderThan, a pending row past its age.
func (w *sweep) remains(ctx context.Context) (bool, error) {
	roots, err := w.store.Directories.Deleting(ctx, w.db, 1)
	if err != nil || len(roots) > 0 || !w.opts.hasPending {
		return len(roots) > 0, err
	}
	rows, err := w.store.Files.pending.All(ctx, w.db, query.Args{"before": w.before, "offset": 0, "fetch": 1})
	if err != nil {
		return false, fmt.Errorf("read the pending files: %w", err)
	}
	return len(rows) > 0, nil
}
