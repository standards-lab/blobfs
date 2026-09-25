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
// pending row may never have been stored. An error stops that file's
// delete and leaves its row deleting, for the next pass to finish.
type ObjectDeleter interface {
	DeleteObject(ctx context.Context, key string) error
}

// SweepResult is what one pass of Store.Sweep did. Files counts the files
// of branches being deleted whose objects it deleted and whose rows it
// purged, Directories the directories it removed, and Stale the stale rows,
// pending or deleting, it reclaimed outside the branches' walks (see
// StaleOlderThan). Each row is counted once, by the step that purged it.
// More reports that the pass stopped with work remaining, at its Batch
// bound or at a row that reached a branch after the branch's mark; a
// caller runs passes while More is true.
type SweepResult struct {
	Files       int
	Directories int
	Stale       int
	More        bool
}

// Sweep runs one bounded pass that finishes the deletes a caller began and
// did not complete: the branches Directories.MarkDeleting marked and, with
// StaleOlderThan, the pending rows of abandoned writes and the deleting
// rows of file deletes that stopped before the purge. The pass is
// stateless: it finds its work in the database each time, and every step
// it takes is idempotent, so a pass stopped at any point, by an error or a
// crash, is finished by the next one. It calls objects for every object it
// deletes and never otherwise touches the object store.
//
// For each branch root Directories.Deleting returns, in id order, the pass
// first marks the branch again, in a transaction of its own, which reaches
// any straggler: a row a create that raced the first mark left active. It
// then walks the branch depth first through the listings with
// IncludeDeleting. In each directory it deletes every file's object and
// then purges the file's row, as the file delete's last two steps do; then
// it empties and removes each child directory the same way; then it
// removes the directory itself, leaf directories before their parents and
// the root last. Each directory is removed in a transaction of its own,
// guarded by the version the pass read it at, with OnRemoveDirectory's
// function run first in the same transaction. A row still active in the
// branch when the pass reaches it, a straggler that landed after this
// pass's mark, stops the branch's walk with More set, and so does a
// directory refused as blobfs.ErrNotEmpty for the same reason; the next
// pass marks the straggler. With StaleOlderThan, the pass spends the budget
// the branches leave on the oldest stale rows (see StaleOlderThan). A
// deleting row inside a branch the walk has not reached yet may be among
// them; it is finished there, as the walk would have finished it, and
// counted once, since the purged row is in no later read.
//
// Batch bounds the records the pass handles. When the pass spends the
// bound, it reads whether any work remains, a branch being deleted or, with
// StaleOlderThan, a stale row past its age, and reports the answer as More.
//
// It takes the *sqlate.DB and not a session because it opens one
// transaction for each mark, each directory's removal, and each pending
// row's delete, which a *sqlate.Tx cannot. It reads and purges on the pool
// and deletes objects outside any transaction, so no row lock is held
// across a call to the object store. A refusal stops the branch or the
// stale row it meets, not the pass. The refusals are an object delete's
// error, a hook's error, and a purge or removal a consumer's foreign key
// refuses as blobfs.ErrReferenced. The pass goes on to the next branch or
// row, so a row refused on every pass does not hold back the work behind
// it, and returns every refusal joined and wrapped as "data: sweep: ...",
// with the result counting what it did. A Batch below 1 and a StaleOlderThan age that is not positive are
// refused before any SQL. Nothing to do is a zero result and no error.
func (s *Store) Sweep(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, opts ...SweepOption) (SweepResult, error) {
	o := sweepOptions{batch: defaultBatch}
	for _, opt := range opts {
		opt(&o)
	}
	switch {
	case o.batch < 1:
		return SweepResult{}, fmt.Errorf("data: sweep: the batch %d is below 1", o.batch)
	case o.hasStale && o.staleAge <= 0:
		return SweepResult{}, fmt.Errorf("data: sweep: the stale age %s is not positive", o.staleAge)
	}
	w := &sweep{store: s, db: db, objects: objects, opts: o, budget: o.batch, refused: map[string]bool{}}
	if o.hasStale {
		w.before = time.Now().Add(-o.staleAge)
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
	// before is the instant a stale row must have been last written
	// before to be reclaimed, set only with StaleOlderThan.
	before time.Time
	budget int
	result SweepResult
	// refused holds the files whose finish this pass tried and was
	// refused, so the stale read does not try them again and report the
	// same refusal twice.
	refused map[string]bool
}

// run is the pass: the branches, then the stale rows with what budget
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
	if w.opts.hasStale && w.budget > 0 {
		errs = append(errs, w.stale(ctx)...)
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
// record of the budget. A row already gone, purged by a concurrent pass
// or its own caller, is done: Purge reports its absence as success. A
// file refused is recorded, so this pass does not try it again.
func (w *sweep) finish(ctx context.Context, f blobfs.File) error {
	if err := w.objects.DeleteObject(ctx, f.Key); err != nil {
		w.refused[f.ID] = true
		return fmt.Errorf("delete the object of file %s: %w", f.ID, err)
	}
	if err := w.store.Files.Purge(ctx, w.db, f.ID); err != nil {
		w.refused[f.ID] = true
		return err
	}
	w.budget--
	return nil
}

// stale reclaims the oldest stale rows last written before w.before, as
// many as the budget allows, each by its status. A pending row is moved
// to deleting in a transaction of its own at the version the pass read,
// then finished; one that moved on since the read, completed or gone, is
// skipped and spends nothing. A deleting row is past that step, so it is
// finished at once. A row refused is skipped too, its refusal returned
// with the others', and so is a row whose finish this pass was already
// refused in a branch's walk.
func (w *sweep) stale(ctx context.Context) []error {
	rows, err := w.store.Files.stale.All(ctx, w.db, query.Args{"before": w.before, "offset": 0, "fetch": w.budget})
	if err != nil {
		return []error{fmt.Errorf("read the stale files: %w", err)}
	}
	var errs []error
	for _, f := range rows {
		if w.refused[f.ID] {
			continue
		}
		file := f
		if f.Status == blobfs.StatusPending {
			file, err = w.db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
				return w.store.Files.Delete(ctx, tx, f.ID, AtVersion(f.Version))
			})
			switch {
			case errors.Is(err, blobfs.ErrNotFound), errors.Is(err, query.ErrVersionMismatch):
				continue
			case err != nil:
				errs = append(errs, fmt.Errorf("stale file %s: %w", f.ID, err))
				continue
			}
		}
		if err := w.finish(ctx, file); err != nil {
			errs = append(errs, fmt.Errorf("stale file %s: %w", f.ID, err))
			continue
		}
		w.result.Stale++
	}
	return errs
}

// remains reports whether work is left for another pass: a branch being
// deleted or, with StaleOlderThan, a stale row past its age.
func (w *sweep) remains(ctx context.Context) (bool, error) {
	roots, err := w.store.Directories.Deleting(ctx, w.db, 1)
	if err != nil || len(roots) > 0 || !w.opts.hasStale {
		return len(roots) > 0, err
	}
	rows, err := w.store.Files.stale.All(ctx, w.db, query.Args{"before": w.before, "offset": 0, "fetch": 1})
	if err != nil {
		return false, fmt.Errorf("read the stale files: %w", err)
	}
	return len(rows) > 0, nil
}
