package datatest

import (
	"errors"
	"sync"
	"testing"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// directories checks the Directories handle's reads, creates, and deletes.
func (s *suite) directories(t *testing.T) {
	t.Run("Root", s.root)
	t.Run("NotFound", s.directoryNotFound)
	t.Run("Create", s.createDirectory)
	t.Run("CreateRefusals", s.createDirectoryRefusals)
	t.Run("Ensure", s.ensureDirectory)
	t.Run("EnsureConcurrent", s.ensureDirectoryConcurrent)
	t.Run("FindByName", s.findDirectoryByName)
	t.Run("Delete", s.deleteDirectory)
}

// root checks the seeded root as the store reads it: the row with
// blobfs.RootID, no parent, and the name /, at version 1, reached by Find
// and by FindByPath of the empty path, with the path /, and never in a
// listing.
func (s *suite) root(t *testing.T) {
	root := s.directory(t, blobfs.RootID)
	if root.ID != blobfs.RootID || root.ParentID != nil || root.Name != "/" || !root.IsRoot() || root.Version != 1 || root.CreatedAt.IsZero() {
		t.Errorf("the root is %+v, want the seeded row", root)
	}
	if d, err := s.store.Directories.FindByPath(s.ctx, s.db, blobfs.RootID, ""); err != nil || !equalDirectory(d, root) {
		t.Errorf("FindByPath(root, \"\") = %+v, %v, want the root", d, err)
	}
	s.wantPath(t, blobfs.RootID, "/")
	s.mkdir(t, "listed-"+t.Name())
	c, err := s.store.Directories.List(s.ctx, s.db, blobfs.RootID, listAll(), firstPage(1000))
	if err != nil {
		t.Fatalf("List(root): %v", err)
	}
	for _, d := range c.Items {
		if d.IsRoot() || d.Name == "/" {
			t.Errorf("the listing of the root holds the root or a row named /: %+v", d)
		}
	}
	if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory WHERE parent_id IS NULL"); n != 1 {
		t.Errorf("%d rows have no parent, want the one seeded root", n)
	}
}

// directoryNotFound checks blobfs.ErrNotFound on every read of a directory
// that does not exist.
func (s *suite) directoryNotFound(t *testing.T) {
	id := blobfs.NewID()
	if _, err := s.store.Directories.Find(s.ctx, s.db, id); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Find(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.store.Directories.FindByName(s.ctx, s.db, blobfs.RootID, "missing-"+id); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("FindByName(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.store.Directories.FindByName(s.ctx, s.db, id, "child"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("FindByName under a missing parent = %v, want ErrNotFound", err)
	}
	if _, err := s.store.Directories.Path(s.ctx, s.db, id); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Path(missing) = %v, want ErrNotFound", err)
	}
	if within, err := s.store.Directories.IsWithin(s.ctx, s.db, id, blobfs.RootID); err != nil || within {
		t.Errorf("IsWithin(missing, root) = %v, %v, want false: a missing directory is within nothing", within, err)
	}
}

// createDirectory checks Create against the baseline: the row as the
// database holds it, with the table's defaults and the name stored
// normalized, the same shape through both stores, the same name allowed
// under another parent, and a file of the same name no conflict.
func (s *suite) createDirectory(t *testing.T) {
	parent := s.mkdir(t, "create-"+t.Name())
	id := blobfs.NewID()
	got, err := s.store.Directories.Create(s.ctx, s.db, parent.ID, decomposed, data.WithID(id))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != id || got.ParentID == nil || *got.ParentID != parent.ID || got.Name != composed || got.IsRoot() || got.Version != 1 ||
		got.CreatedAt.IsZero() || !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("Create returned %+v, want the row at version 1 under the composed name with the table's defaults", got)
	}
	if stored := s.directory(t, id); !equalDirectory(got, stored) {
		t.Errorf("Create returned\n%+v\nbut the database holds\n%+v", got, stored)
	}
	base, err := s.baseline.Directories.Create(s.ctx, s.db, parent.ID, "base")
	if err != nil {
		t.Fatalf("the baseline's Create: %v", err)
	}
	if *base.ParentID != *got.ParentID || base.Version != got.Version || !base.UpdatedAt.Equal(base.CreatedAt) {
		t.Errorf("the row\n%+v\ndiffers in shape from the baseline's\n%+v", got, base)
	}
	if _, err := s.store.Directories.Create(s.ctx, s.db, got.ID, composed); err != nil {
		t.Errorf("Create of the same name under another parent: %v", err)
	}
	s.insertFile(t, parent.ID, "shared", blobfs.StatusAvailable)
	if _, err := s.store.Directories.Create(s.ctx, s.db, parent.ID, "shared"); err != nil {
		t.Errorf("Create under the name of a file: %v; the name spaces are separate", err)
	}
}

// createDirectoryRefusals checks Create's and Ensure's refusals against the
// baseline: a taken name in either spelling, a missing parent, and a taken
// id, each by its sentinel over its constraint in the same text on both
// stores, Ensure finding the taken name instead of refusing it; and the
// refusals before any SQL, an invalid name and an invalid id, which leave
// no row.
func (s *suite) createDirectoryRefusals(t *testing.T) {
	parent := s.mkdir(t, "refusals-"+t.Name())
	taken := s.mkdirUnder(t, parent.ID, composed)
	for _, c := range []struct {
		name       string
		parent     string
		dir        string
		opts       []data.CreateOption
		want       error
		constraint string
	}{
		{"NameTaken", parent.ID, composed, nil, blobfs.ErrNameTaken, blobfs.ConstraintUniqueDirectoryParentName},
		{"NameTakenDecomposed", parent.ID, decomposed, nil, blobfs.ErrNameTaken, blobfs.ConstraintUniqueDirectoryParentName},
		{"MissingParent", blobfs.NewID(), "orphan", nil, blobfs.ErrNotFound, blobfs.ConstraintForeignKeyDirectoryParent},
		{"IDTaken", parent.ID, "twin", []data.CreateOption{data.WithID(taken.ID)}, blobfs.ErrIDTaken, blobfs.ConstraintPrimaryKeyDirectory},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.store.Directories.Create(s.ctx, s.db, c.parent, c.dir, c.opts...)
			wantViolation(t, err, c.want, c.constraint)
			_, base := s.baseline.Directories.Create(s.ctx, s.db, c.parent, c.dir, c.opts...)
			wantSameError(t, err, base)
			found, created, err := s.store.Directories.Ensure(s.ctx, s.db, c.parent, c.dir, c.opts...)
			_, _, base = s.baseline.Directories.Ensure(s.ctx, s.db, c.parent, c.dir, c.opts...)
			if c.want == blobfs.ErrNameTaken {
				// The lookup runs first, so the taken name is found.
				if err != nil || base != nil || created || !equalDirectory(found, taken) {
					t.Errorf("Ensure of a taken name = %+v, %v, %v and %v on the baseline, want the row found", found, created, err, base)
				}
				return
			}
			wantViolation(t, err, c.want, c.constraint)
			wantSameError(t, err, base)
		})
	}
	t.Run("BeforeSQL", func(t *testing.T) {
		for _, c := range []struct {
			dir  string
			opts []data.CreateOption
			want error
		}{
			{"no/slash", nil, blobfs.ErrInvalidName},
			{"", nil, blobfs.ErrInvalidName},
			{"..", nil, blobfs.ErrInvalidName},
			{"bad-id", []data.CreateOption{data.WithID(blobfs.RootID)}, blobfs.ErrInvalidID},
			{"bad-id", []data.CreateOption{data.WithID("not-a-uuid")}, blobfs.ErrInvalidID},
		} {
			_, err := s.store.Directories.Create(s.ctx, s.db, parent.ID, c.dir, c.opts...)
			if !errors.Is(err, c.want) {
				t.Errorf("Create(%q) = %v, want %v", c.dir, err, c.want)
			}
			_, _, err = s.store.Directories.Ensure(s.ctx, s.db, parent.ID, c.dir, c.opts...)
			if !errors.Is(err, c.want) {
				t.Errorf("Ensure(%q) = %v, want %v", c.dir, err, c.want)
			}
		}
		if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory WHERE parent_id = "+s.db.Dialect().Placeholder(1), parent.ID); n != 1 {
			t.Errorf("%d directories under the parent after the refusals, want the one taken", n)
		}
	})
}

// ensureDirectory checks Ensure on both stores: the first call creates
// the row under a supplied id, and later calls in either spelling, under
// another supplied id, and inside a transaction find the same row and
// create nothing.
func (s *suite) ensureDirectory(t *testing.T) {
	parent := s.mkdir(t, "ensure-"+t.Name())
	for _, tier := range []struct {
		name  string
		store *data.Store
	}{{"UnderTest", s.store}, {"Baseline", s.baseline}} {
		t.Run(tier.name, func(t *testing.T) {
			id := blobfs.NewID()
			n := decomposed + "-" + tier.name
			created, made, err := tier.store.Directories.Ensure(s.ctx, s.db, parent.ID, n, data.WithID(id))
			if err != nil || !made || created.ID != id || created.Name != blobfs.NormalizeName(n) || created.Version != 1 {
				t.Fatalf("the first Ensure = %+v, %v, %v; want the row created under the id and the composed name", created, made, err)
			}
			for _, spelling := range []string{n, blobfs.NormalizeName(n)} {
				found, made, err := tier.store.Directories.Ensure(s.ctx, s.db, parent.ID, spelling, data.WithID(blobfs.NewID()))
				if err != nil || made || !equalDirectory(created, found) {
					t.Errorf("Ensure(%q) again = %+v, %v, %v; want the same row found", spelling, found, made, err)
				}
			}
			inTx, err := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
				found, made, err := tier.store.Directories.Ensure(s.ctx, tx, parent.ID, n)
				if err == nil && made {
					return found, errors.New("the lookup created a second row")
				}
				return found, err
			})
			if err != nil || !equalDirectory(created, inTx) {
				t.Errorf("Ensure inside a transaction = %+v, %v; want the row found", inTx, err)
			}
		})
	}
}

// ensureDirectoryConcurrent checks the race on the pool: two callers
// ensure the same name at once, exactly one creates it, both return the
// same row, and the parent holds one row of the name. The engine blocks
// the second insert on the unique constraint until the first commits and
// then refuses it, and the second caller recovers by looking the row up.
func (s *suite) ensureDirectoryConcurrent(t *testing.T) {
	parent := s.mkdir(t, "ensure-concurrent-"+t.Name())
	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		results []blobfs.Directory
		creates int
	)
	start.Add(1)
	for range 2 {
		done.Go(func() {
			start.Wait()
			d, created, err := s.store.Directories.Ensure(s.ctx, s.db, parent.ID, "shared")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("Ensure: %v", err)
				return
			}
			results = append(results, d)
			if created {
				creates++
			}
		})
	}
	start.Done()
	done.Wait()
	if len(results) != 2 || !equalDirectory(results[0], results[1]) || creates != 1 {
		t.Errorf("the two callers got %+v with %d creates; want the same row and one create", results, creates)
	}
	if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory WHERE parent_id = "+s.db.Dialect().Placeholder(1), parent.ID); n != 1 {
		t.Errorf("%d rows under the parent, want one", n)
	}
}

// findDirectoryByName checks FindByName: either spelling finds the row
// stored composed, a file of the same name is not found, and a name
// ValidateName refuses is a NameError before any SQL.
func (s *suite) findDirectoryByName(t *testing.T) {
	parent := s.mkdir(t, "by-name-"+t.Name())
	cafe := s.mkdirUnder(t, parent.ID, composed)
	for _, spelling := range []string{composed, decomposed} {
		if d, err := s.store.Directories.FindByName(s.ctx, s.db, parent.ID, spelling); err != nil || !equalDirectory(d, cafe) {
			t.Errorf("FindByName(%q) = %+v, %v, want %+v", spelling, d, err, cafe)
		}
	}
	s.insertFile(t, parent.ID, "file-only", blobfs.StatusAvailable)
	if _, err := s.store.Directories.FindByName(s.ctx, s.db, parent.ID, "file-only"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("FindByName of a file's name = %v, want ErrNotFound", err)
	}
	var nameErr *blobfs.NameError
	if _, err := s.store.Directories.FindByName(s.ctx, s.db, parent.ID, "a/b"); !errors.As(err, &nameErr) {
		t.Errorf("FindByName(a/b) = %v, want a NameError", err)
	}
}

// deleteDirectory checks Delete against the schema: the root refused
// before any SQL; a directory with a child directory, with a file, or
// with a file whose delete has begun refused as ErrNotEmpty under the
// foreign key that names what remains; a directory a consumer's row
// references refused as ErrReferenced under the consumer's constraint; a
// missing directory ErrNotFound; and an empty directory removed, after
// which its name is free again.
func (s *suite) deleteDirectory(t *testing.T) {
	if err := s.store.Directories.Delete(s.ctx, s.db, blobfs.RootID); !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("Delete(root) = %v, want ErrRootDirectory", err)
	}
	s.directory(t, blobfs.RootID)

	parent := s.mkdir(t, "delete-"+t.Name())
	child := s.mkdirUnder(t, parent.ID, "child")
	err := s.store.Directories.Delete(s.ctx, s.db, parent.ID)
	wantViolation(t, err, blobfs.ErrNotEmpty, blobfs.ConstraintForeignKeyDirectoryParent)
	wantSameError(t, err, s.baseline.Directories.Delete(s.ctx, s.db, parent.ID))
	if err := s.store.Directories.Delete(s.ctx, s.db, child.ID); err != nil {
		t.Fatalf("Delete of the empty child: %v", err)
	}

	id := s.insertFile(t, parent.ID, "a.txt", blobfs.StatusAvailable)
	wantViolation(t, s.store.Directories.Delete(s.ctx, s.db, parent.ID), blobfs.ErrNotEmpty, blobfs.ConstraintForeignKeyFileDirectory)
	s.beginDelete(t, id)
	if err := s.store.Directories.Delete(s.ctx, s.db, parent.ID); !errors.Is(err, blobfs.ErrNotEmpty) {
		t.Errorf("Delete while a file is deleting = %v, want ErrNotEmpty: a deleting row keeps its slot", err)
	}
	s.purge(t, id)

	s.createDirectoryReferences(t)
	ph := s.db.Dialect().Placeholder
	s.exec(t, "INSERT INTO datatest_owner (directory_id) VALUES ("+ph(1)+")", parent.ID)
	err = s.store.Directories.Delete(s.ctx, s.db, parent.ID)
	wantViolation(t, err, blobfs.ErrReferenced, ownerConstraint)
	var ce *sqlate.ConstraintError
	if errors.Is(err, blobfs.ErrNotEmpty) || !errors.As(err, &ce) || !errors.Is(ce.Class, sqlate.ErrForeignKeyViolation) {
		t.Errorf("Delete of a directory a consumer references = %v, want ErrReferenced by the violation's class and not ErrNotEmpty", err)
	}
	s.exec(t, "DELETE FROM datatest_owner WHERE directory_id = "+ph(1), parent.ID)

	if err := s.store.Directories.Delete(s.ctx, s.db, parent.ID); err != nil {
		t.Fatalf("Delete of the emptied directory: %v", err)
	}
	if _, err := s.store.Directories.Find(s.ctx, s.db, parent.ID); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Find after the delete = %v, want ErrNotFound", err)
	}
	err = s.store.Directories.Delete(s.ctx, s.db, parent.ID)
	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Delete of the deleted directory = %v, want ErrNotFound", err)
	}
	wantSameError(t, err, s.baseline.Directories.Delete(s.ctx, s.db, parent.ID))
	if _, err := s.store.Directories.Create(s.ctx, s.db, blobfs.RootID, parent.Name); err != nil {
		t.Errorf("Create under the freed name: %v", err)
	}
}
