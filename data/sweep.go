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

// ObjectDeleter is the consumer's object store as Store.Sweep calls it.
// DeleteObject must be idempotent, a missing object being success, since a
// pass may delete an object again and a pending row's object may never
// have been stored. An error leaves that file's row deleting for the next
// pass.
type ObjectDeleter interface {
	DeleteObject(ctx context.Context, key string) error
}

// SweepResult is what one pass of Store.Sweep did: Files counts the
// branches' files finished, Directories the directories removed, and Stale
// the stale rows reclaimed, each row once. More reports work remaining; a
// caller runs passes while it is true.
type SweepResult struct {
	Files       int
	Directories int
	Stale       int
	More        bool
}

// Sweep runs one bounded, stateless pass that finishes the deletes callers
// began: for each branch root Directories.Deleting returns, it marks the
// branch again and walks it, deleting each file's object through objects,
// purging its row, and removing each directory once empty; with
// StaleOlderThan it then reclaims stale rows. It takes the *sqlate.DB
// because it opens a transaction of its own for each mark, removal, and
// pending row's delete, and holds none across a call to objects. See The
// sweep in docs/features.md.
//
// A refusal stops the branch or row it meets, not the pass: an object
// delete's error, the hook's error, or blobfs.ErrReferenced from a
// consumer's foreign key. The pass returns every refusal joined, with the
// result counting what it did. A Batch below 1 and a StaleOlderThan age
// that is not positive are refused before any SQL.
func (s *Store) Sweep(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, opts ...SweepOption) (_ SweepResult, err error) {
	defer wrap(&err, "sweep")
	o := sweepOptions{batch: defaultBatch}
	for _, opt := range opts {
		opt(&o)
	}
	switch {
	case o.batch < 1:
		return SweepResult{}, fmt.Errorf("the batch %d is below 1", o.batch)
	case o.hasStale && o.staleAge <= 0:
		return SweepResult{}, fmt.Errorf("the stale age %s is not positive", o.staleAge)
	}
	w := &sweep{store: s, db: db, objects: objects, opts: o, budget: o.batch, refused: map[string]bool{}}
	if o.hasStale {
		w.before = time.Now().Add(-o.staleAge)
	}
	return w.result, w.run(ctx)
}

// sweep is one pass's state.
type sweep struct {
	store   *Store
	db      *sqlate.DB
	objects ObjectDeleter
	opts    sweepOptions
	// before is the stale rows' cutoff, set only with StaleOlderThan.
	before time.Time
	budget int
	result SweepResult
	// refused holds the files this pass was refused, so the stale read
	// does not report the same refusal twice.
	refused map[string]bool
}

// run is the pass: the branches, then the stale rows, then, when the
// budget is spent, the read of whether work remains.
func (w *sweep) run(ctx context.Context) error {
	roots, err := w.roots(ctx, w.budget)
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

// branch repeats the mark of root's branch and walks it.
func (w *sweep) branch(ctx context.Context, root blobfs.Directory) error {
	_, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (Marked, error) {
		return w.store.Directories.markDeleting(ctx, tx, root.ID, nil)
	})
	switch {
	case errors.Is(err, blobfs.ErrNotFound):
		// A concurrent pass removed the root since the roots were read.
		return nil
	case err != nil:
		return fmt.Errorf("mark directory %s deleting: %w", root.ID, err)
	}
	_, err = w.directory(ctx, root)
	return err
}

// directory empties and removes dir, files first, then child directories,
// and reports whether dir is gone. It stops at the budget, and at an
// active row, a straggler, with More set.
func (w *sweep) directory(ctx context.Context, dir blobfs.Directory) (bool, error) {
	for {
		if w.budget == 0 {
			return false, nil
		}
		page, err := w.store.Files.list.list(ctx, w.db, dir.ID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: w.budget}, []ListOption{IncludeDeleting()})
		if err != nil {
			return false, fmt.Errorf("list files in %s: %w", dir.ID, err)
		}
		for _, f := range page.Items {
			if f.Status.Mutable() {
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
		page, err := w.store.Directories.list.list(ctx, w.db, dir.ID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: w.budget}, []ListOption{IncludeDeleting()})
		if err != nil {
			return false, fmt.Errorf("list directories under %s: %w", dir.ID, err)
		}
		if len(page.Items) == 0 {
			break
		}
		for _, child := range page.Items {
			if child.Status.Mutable() {
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

// remove removes the emptied dir after the hook, in one transaction, at
// the version the pass read it at. A directory a straggler landed in is
// left, with More set.
func (w *sweep) remove(ctx context.Context, dir blobfs.Directory) (bool, error) {
	var hookErr error
	_, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (struct{}, error) {
		if w.opts.onRemove != nil {
			if hookErr = w.opts.onRemove(ctx, tx, dir); hookErr != nil {
				return struct{}{}, hookErr
			}
		}
		return struct{}{}, w.store.Directories.deleteDirectory(ctx, tx, dir.ID, &dir.Version)
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
		return false, fmt.Errorf("delete directory %s: %w", dir.ID, err)
	}
	w.result.Directories++
	w.budget--
	return true, nil
}

// finish deletes the deleting file f's object, purges its row, and spends
// one record of the budget; a refused file is recorded.
func (w *sweep) finish(ctx context.Context, f blobfs.File) error {
	if err := w.objects.DeleteObject(ctx, f.Key); err != nil {
		w.refused[f.ID] = true
		return fmt.Errorf("delete the object of file %s: %w", f.ID, err)
	}
	if err := w.store.Files.purgeFile(ctx, w.db, f.ID); err != nil {
		w.refused[f.ID] = true
		return fmt.Errorf("purge file %s: %w", f.ID, err)
	}
	w.budget--
	return nil
}

// stale reclaims the oldest stale rows, as many as the budget allows: a
// pending row is moved to deleting at the version read, and skipped if it
// moved on; then each row is finished.
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
				return w.store.Files.deleteFile(ctx, tx, f.ID, &f.Version)
			})
			switch {
			case errors.Is(err, blobfs.ErrNotFound), errors.Is(err, query.ErrVersionMismatch):
				continue
			case err != nil:
				errs = append(errs, fmt.Errorf("stale file %s: delete: %w", f.ID, err))
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

// remains reports whether a branch or, with StaleOlderThan, a stale row
// is left for another pass.
func (w *sweep) remains(ctx context.Context) (bool, error) {
	roots, err := w.roots(ctx, 1)
	if err != nil || len(roots) > 0 || !w.opts.hasStale {
		return len(roots) > 0, err
	}
	rows, err := w.store.Files.stale.All(ctx, w.db, query.Args{"before": w.before, "offset": 0, "fetch": 1})
	if err != nil {
		return false, fmt.Errorf("read the stale files: %w", err)
	}
	return len(rows) > 0, nil
}

// roots reads at most limit branch roots, as Directories.Deleting does.
func (w *sweep) roots(ctx context.Context, limit int) ([]blobfs.Directory, error) {
	roots, err := w.store.Directories.deleting.All(ctx, w.db, query.Args{"offset": 0, "fetch": limit})
	if err != nil {
		return nil, fmt.Errorf("read the deleting branches: %w", err)
	}
	return roots, nil
}
