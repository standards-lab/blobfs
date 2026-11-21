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

// ObjectDeleter is the consumer's object store as Store.Sweep and the
// delete protocols call it. DeleteObject must treat a missing object as
// success, since a step may run again.
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
// began: the marked branches, and with SweepOptions.StaleOlderThan the
// stale rows. A refusal leaves its row for a later pass, and the pass
// returns every refusal joined. See The sweep in docs/features.md.
func (s *Store) Sweep(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, opts SweepOptions) (_ SweepResult, err error) {
	defer wrap(&err, "sweep")
	switch {
	case opts.Batch < 0:
		return SweepResult{}, fmt.Errorf("the batch %d is below 1", opts.Batch)
	case opts.StaleOlderThan < 0:
		return SweepResult{}, fmt.Errorf("the stale age %s is not positive", opts.StaleOlderThan)
	}
	w := &sweep{store: s, db: db, objects: objects, opts: opts, budget: opts.batch(), refusedFiles: map[string]bool{}}
	if opts.stale() {
		w.staleBefore = time.Now().Add(-opts.StaleOlderThan)
	}
	err = w.run(ctx)
	return w.result, err
}

// SweepUntilDone runs pass, the consumer's closure over one Store.Sweep,
// while it reports More, and hands each result to report. It returns nil
// once the work is done or stop closes, and ctx's error once ctx ends. A
// pass's error never ends the loop. See The sweep in docs/features.md.
func SweepUntilDone(ctx context.Context, stop <-chan struct{}, pass func(context.Context) (SweepResult, error), report func(SweepResult, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-stop:
			return nil
		default:
		}
		res, err := pass(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if report != nil {
			report(res, err)
		}
		if !res.More {
			return nil
		}
	}
}

type sweep struct {
	store                *Store
	db                   *sqlate.DB
	objects              ObjectDeleter
	opts                 SweepOptions
	staleBefore          time.Time
	budget               int
	result               SweepResult
	errs                 []error
	refusedFiles         map[string]bool
	leftRoots, leftStale int
	root                 blobfs.Directory
	remarked             bool
}

type walk int

const (
	removed walk = iota
	emptied
	kept
	straggler
	stopped
)

func (w *sweep) run(ctx context.Context) error {
	if err := w.branches(ctx); err != nil {
		return errors.Join(append(w.errs, err)...)
	}
	if w.opts.stale() && w.budget > 0 {
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

func (w *sweep) branch(ctx context.Context, root blobfs.Directory) walk {
	w.root, w.remarked = root, false
	end, err := w.directory(ctx, root)
	if err != nil {
		w.refuse(err)
	}
	return end
}

func (w *sweep) refuse(err error) {
	w.errs = append(w.errs, fmt.Errorf("branch %s: %w", w.root.ID, err))
}

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
			case w.refusedFiles[f.ID]:
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

func (w *sweep) files(ctx context.Context, dir string, after query.Cursor) (query.Collection[blobfs.File], error) {
	return walkPage(ctx, w.store.Files.list, w.db, dir, after, w.budget)
}

func (w *sweep) children(ctx context.Context, dir string, after query.Cursor) (query.Collection[blobfs.Directory], error) {
	return walkPage(ctx, w.store.Directories.list, w.db, dir, after, w.budget)
}

func walkPage[T any](ctx context.Context, l listing[T], sess sqlate.Session, id string, after query.Cursor, size int) (query.Collection[T], error) {
	req := query.Directives{Total: query.TotalNone}
	include := ListOptions{IncludeDeleting: true}
	if after == "" {
		return l.list(ctx, sess, id, req, query.Page{Number: 1, Size: size}, include)
	}
	return l.cont(ctx, sess, id, req, CursorPage{After: after, Size: size}, include)
}

func (w *sweep) remark(ctx context.Context) (bool, error) {
	w.remarked = true
	_, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (Marked, error) {
		return w.store.Directories.markDeleting(ctx, tx, w.root.ID, nil)
	}, nil)
	switch {
	case errors.Is(err, blobfs.ErrNotFound):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("mark directory %s deleting: %w", w.root.ID, err)
	}
	return false, nil
}

func (w *sweep) remove(ctx context.Context, dir blobfs.Directory) (walk, error) {
	var hookErr error
	_, err := w.db.Transact(ctx, func(tx *sqlate.Tx) (struct{}, error) {
		if w.opts.OnRemoveDirectory != nil {
			if hookErr = w.opts.OnRemoveDirectory(ctx, tx, dir); hookErr != nil {
				return struct{}{}, hookErr
			}
		}
		return struct{}{}, w.store.Directories.deleteDirectory(ctx, tx, dir.ID, &dir.Version)
	}, nil)
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

func (w *sweep) finish(ctx context.Context, f blobfs.File) error {
	if err := w.objects.DeleteObject(ctx, f.Key); err != nil {
		w.refusedFiles[f.ID] = true
		return fmt.Errorf("delete the object of file %s: %w", f.ID, err)
	}
	if err := w.store.Files.purgeFile(ctx, w.db, f.ID); err != nil {
		w.refusedFiles[f.ID] = true
		return fmt.Errorf("purge file %s: %w", f.ID, err)
	}
	w.budget--
	return nil
}

func (w *sweep) stale(ctx context.Context) error {
	for w.budget > 0 {
		fetch := w.budget
		rows, err := w.store.Files.stale.All(ctx, w.db, query.Args{"before": w.staleBefore, "offset": w.leftStale, "fetch": fetch})
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

func (w *sweep) reclaim(ctx context.Context, f blobfs.File) bool {
	if w.refusedFiles[f.ID] {
		return false
	}
	file := f
	if f.Status == blobfs.StatusPending {
		var err error
		file, err = w.db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
			file, _, err := w.store.Files.deleteFile(ctx, tx, f.ID, &f.Version)
			return file, err
		}, nil)
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

func (w *sweep) remains(ctx context.Context) (bool, error) {
	roots, err := w.roots(ctx, w.leftRoots, 1)
	if err != nil || len(roots) > 0 || !w.opts.stale() {
		return len(roots) > 0, err
	}
	rows, err := w.store.Files.stale.All(ctx, w.db, query.Args{"before": w.staleBefore, "offset": w.leftStale, "fetch": 1})
	if err != nil {
		return false, fmt.Errorf("read the stale files: %w", err)
	}
	return len(rows) > 0, nil
}

func (w *sweep) roots(ctx context.Context, offset, limit int) ([]blobfs.Directory, error) {
	roots, err := w.store.Directories.deleting.All(ctx, w.db, query.Args{"offset": offset, "fetch": limit})
	if err != nil {
		return nil, fmt.Errorf("read the deleting branches: %w", err)
	}
	return roots, nil
}
