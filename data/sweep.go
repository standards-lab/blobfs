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
// began: for each branch root Directories.Deleting returns, it walks the
// branch, deleting each file's object through objects, purging its row,
// and removing each directory once empty, and marks the branch again only
// when the walk meets a straggler; with StaleOlderThan it then reclaims
// stale rows. It takes the *sqlate.DB because it opens a transaction of
// its own for each mark, removal, and pending row's delete, and holds none
// across a call to objects. See The sweep in docs/features.md.
//
// A refusal leaves the row it meets, and the directories above it, for a
// later pass, and the pass goes on past it: an object delete's error, the
// hook's error, or blobfs.ErrReferenced from a consumer's foreign key. The
// pass returns every refusal joined, with the result counting what it
// did. A Batch below 1 and a StaleOlderThan age that is not positive are
// refused before any SQL.
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
	err = w.run(ctx)
	return w.result, err
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
	// errs are the pass's refusals, joined when it ends.
	errs []error
	// refused holds the files this pass was refused, so neither a second
	// walk of their directory nor the stale read tries them again.
	refused map[string]bool
	// leftRoots and leftStale count the roots and stale rows this pass read
	// and left in place, the offset each next read of them starts at, so a
	// row refused on every pass holds back nothing behind it.
	leftRoots, leftStale int
	// root is the branch being walked, and remarked whether this pass has
	// marked it again.
	root     blobfs.Directory
	remarked bool
}

// walk is how the walk of a directory ended.
type walk int

const (
	// removed is a directory gone, by this pass or another.
	removed walk = iota
	// emptied is a directory whose contents are gone, ready for removal.
	emptied
	// kept is a directory a refusal inside it, or of it, keeps; the walk
	// goes on with its siblings.
	kept
	// straggler is a directory holding an active row, or refused as not
	// empty.
	straggler
	// stopped ends the branch's walk: the budget is spent, a straggler is
	// left, or a read failed.
	stopped
)

// run is the pass: the branches, then the stale rows, then, when the
// budget is spent and nothing has shown that work remains, the read of
// whether it does.
func (w *sweep) run(ctx context.Context) error {
	if err := w.branches(ctx); err != nil {
		return errors.Join(append(w.errs, err)...)
	}
	if w.opts.hasStale && w.budget > 0 {
		if err := w.stale(ctx); err != nil {
			w.errs = append(w.errs, err)
		}
	}
	if w.budget == 0 && !w.result.More {
		more, err := w.remains(ctx)
		if err != nil {
			w.errs = append(w.errs, err)
		}
		w.result.More = more
	}
	return errors.Join(w.errs...)
}

// branches walks the branches in id order, reading their roots a page at
// a time past the roots it left, until the budget is spent or a short page
// shows no root remains. A root it did not reach sets More.
func (w *sweep) branches(ctx context.Context) error {
	for w.budget > 0 {
		fetch := w.budget
		roots, err := w.roots(ctx, w.leftRoots, fetch)
		if err != nil {
			return err
		}
		for _, root := range roots {
			if w.budget == 0 {
				w.result.More = true
				return nil
			}
			if w.branch(ctx, root) != removed {
				w.leftRoots++
			}
		}
		if len(roots) < fetch {
			return nil
		}
	}
	return nil
}

// branch walks root's branch and reports how the root's walk ended.
func (w *sweep) branch(ctx context.Context, root blobfs.Directory) walk {
	w.root, w.remarked = root, false
	end, err := w.directory(ctx, root)
	if err != nil {
		w.refuse(err)
	}
	return end
}

// refuse records err against the branch being walked.
func (w *sweep) refuse(err error) {
	w.errs = append(w.errs, fmt.Errorf("branch %s: %w", w.root.ID, err))
}

// directory empties and removes dir, files first, then child directories.
// A straggler has the branch marked again, once a pass, and dir walked
// again, once more after that; a straggler left then stops the branch
// with More set.
func (w *sweep) directory(ctx context.Context, dir blobfs.Directory) (walk, error) {
	for again := false; ; again = true {
		end, err := w.contents(ctx, dir)
		if err == nil && end == emptied {
			end, err = w.remove(ctx, dir)
		}
		switch {
		case err != nil:
			return stopped, err
		case end != straggler:
			return end, nil
		case !w.remarked:
			switch gone, err := w.remark(ctx); {
			case err != nil:
				return stopped, err
			case gone:
				return removed, nil
			}
		case again:
			w.result.More = true
			return stopped, nil
		}
	}
}

// contents empties dir: each file finished, then each child directory
// walked, both read a page at a time in name order past the last row
// read. It stops at the budget with More set, and at an active row.
func (w *sweep) contents(ctx context.Context, dir blobfs.Directory) (walk, error) {
	if w.budget == 0 {
		w.result.More = true
		return stopped, nil
	}
	end := emptied
	for page, err := w.files(ctx, dir.ID, ""); ; page, err = w.files(ctx, dir.ID, page.Next) {
		if err != nil {
			return stopped, fmt.Errorf("list files in %s: %w", dir.ID, err)
		}
		for _, f := range page.Items {
			switch {
			case w.refused[f.ID]:
				end = kept
			case f.Status.Mutable():
				return straggler, nil
			default:
				if err := w.finish(ctx, f); err != nil {
					w.refuse(err)
					end = kept
					continue
				}
				w.result.Files++
			}
		}
		if !page.More {
			break
		}
		if w.budget == 0 {
			w.result.More = true
			return stopped, nil
		}
	}
	if w.budget == 0 {
		w.result.More = true
		return stopped, nil
	}
	for page, err := w.children(ctx, dir.ID, ""); ; page, err = w.children(ctx, dir.ID, page.Next) {
		if err != nil {
			return stopped, fmt.Errorf("list directories under %s: %w", dir.ID, err)
		}
		for _, child := range page.Items {
			if child.Status.Mutable() {
				return straggler, nil
			}
			switch sub, err := w.directory(ctx, child); {
			case err != nil || sub == stopped:
				return stopped, err
			case sub == kept:
				end = kept
			}
		}
		if !page.More {
			break
		}
		if w.budget == 0 {
			w.result.More = true
			return stopped, nil
		}
	}
	if end == emptied && w.budget == 0 {
		w.result.More = true
		return stopped, nil
	}
	return end, nil
}

// files reads the page of dir's files, deleting ones included, past
// after, or the first page when after is empty, as many as the budget.
func (w *sweep) files(ctx context.Context, dir string, after query.Cursor) (query.Collection[blobfs.File], error) {
	return walkPage(ctx, w.store.Files.list, w.db, dir, after, w.budget)
}

// children reads the page of dir's child directories, deleting ones
// included, past after, or the first page when after is empty, as many as
// the budget.
func (w *sweep) children(ctx context.Context, dir string, after query.Cursor) (query.Collection[blobfs.Directory], error) {
	return walkPage(ctx, w.store.Directories.list, w.db, dir, after, w.budget)
}

// walkPage reads size rows of l anchored on id, every status, in name
// order past after, or the first page when after is empty.
func walkPage[T any](ctx context.Context, l listing[T], sess sqlate.Session, id string, after query.Cursor, size int) (query.Collection[T], error) {
	req := query.Directives{Total: query.TotalNone}
	include := []ListOption{IncludeDeleting()}
	if after == "" {
		return l.list(ctx, sess, id, req, query.Page{Number: 1, Size: size}, include)
	}
	return l.cont(ctx, sess, id, req, after, size, include)
}

// remark marks the branch again after a straggler, in a transaction of its
// own, and reports whether the branch is gone, removed by a concurrent
// pass since the walk began.
func (w *sweep) remark(ctx context.Context) (bool, error) {
	w.remarked = true
	_, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (Marked, error) {
		return w.store.Directories.markDeleting(ctx, tx, w.root.ID, nil)
	})
	switch {
	case errors.Is(err, blobfs.ErrNotFound):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("mark directory %s deleting: %w", w.root.ID, err)
	}
	return false, nil
}

// remove removes the emptied dir after the hook, in one transaction, at
// the version the pass read it at. A directory a straggler landed in is
// reported as one; a refused removal keeps the directory.
func (w *sweep) remove(ctx context.Context, dir blobfs.Directory) (walk, error) {
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
		w.refuse(fmt.Errorf("remove directory %s: the hook: %w", dir.ID, err))
		return kept, nil
	case errors.Is(err, blobfs.ErrNotFound):
		return removed, nil
	case errors.Is(err, blobfs.ErrNotEmpty):
		return straggler, nil
	case err != nil:
		w.refuse(fmt.Errorf("delete directory %s: %w", dir.ID, err))
		return kept, nil
	}
	w.result.Directories++
	w.budget--
	return removed, nil
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

// stale reclaims the oldest stale rows, as many as the budget allows,
// reading them a page at a time past the rows it left, until the budget is
// spent or a short page shows none remain.
func (w *sweep) stale(ctx context.Context) error {
	for w.budget > 0 {
		fetch := w.budget
		rows, err := w.store.Files.stale.All(ctx, w.db, query.Args{"before": w.before, "offset": w.leftStale, "fetch": fetch})
		if err != nil {
			return fmt.Errorf("read the stale files: %w", err)
		}
		for _, f := range rows {
			if !w.reclaim(ctx, f) {
				w.leftStale++
			}
		}
		if len(rows) < fetch {
			return nil
		}
	}
	return nil
}

// reclaim finishes the stale row f and reports whether it left the stale
// rows: a pending row is moved to deleting at the version read, and
// skipped if it is gone or moved on; then the row is finished. A row this
// pass was refused, here or in a branch's walk, is left.
func (w *sweep) reclaim(ctx context.Context, f blobfs.File) bool {
	if w.refused[f.ID] {
		return false
	}
	file := f
	if f.Status == blobfs.StatusPending {
		var err error
		file, err = w.db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
			return w.store.Files.deleteFile(ctx, tx, f.ID, &f.Version)
		})
		switch {
		case errors.Is(err, blobfs.ErrNotFound), errors.Is(err, query.ErrVersionMismatch):
			return true
		case err != nil:
			w.errs = append(w.errs, fmt.Errorf("stale file %s: delete: %w", f.ID, err))
			return false
		}
	}
	if err := w.finish(ctx, file); err != nil {
		w.errs = append(w.errs, fmt.Errorf("stale file %s: %w", f.ID, err))
		return false
	}
	w.result.Stale++
	return true
}

// remains reports whether a branch or, with StaleOlderThan, a stale row
// is left for another pass past those this pass left.
func (w *sweep) remains(ctx context.Context) (bool, error) {
	roots, err := w.roots(ctx, w.leftRoots, 1)
	if err != nil || len(roots) > 0 || !w.opts.hasStale {
		return len(roots) > 0, err
	}
	rows, err := w.store.Files.stale.All(ctx, w.db, query.Args{"before": w.before, "offset": w.leftStale, "fetch": 1})
	if err != nil {
		return false, fmt.Errorf("read the stale files: %w", err)
	}
	return len(rows) > 0, nil
}

// roots reads at most limit branch roots past offset, as
// Directories.Deleting does.
func (w *sweep) roots(ctx context.Context, offset, limit int) ([]blobfs.Directory, error) {
	roots, err := w.store.Directories.deleting.All(ctx, w.db, query.Args{"offset": offset, "fetch": limit})
	if err != nil {
		return nil, fmt.Errorf("read the deleting branches: %w", err)
	}
	return roots, nil
}
