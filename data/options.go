package data

// Option configures New beyond its required arguments.
type Option func(*options)

// options collects what the options set.
type options struct {
	engine Engine
}

// WithEngine makes the store forward its variation points to the variant
// e builds instead of Standard. New calls e once, with its own catalog and
// dialect and the baseline it bound over the statements it compiled, so a
// consumer selects an engine with New(catalog, dialect,
// WithEngine(postgres.Engine)), or passes an Engine of its own. See
// Engine.
func WithEngine(e Engine) Option {
	return func(o *options) { o.engine = e }
}

// CreateOption configures one call of an operation that creates a row,
// Create or Ensure, beyond its required arguments.
type CreateOption func(*createOptions)

// createOptions collects what the create options set.
type createOptions struct {
	id    string
	hasID bool
}

// WithID supplies the id of the row the call inserts, in place of one the
// store mints, so a seeded row keeps the same id across resets. The id is
// checked with blobfs.ParseID before any SQL: text that is not a UUID, or
// the nil UUID, is blobfs.ErrInvalidID, and the canonical form is what the
// row carries. An id a row of the same table already carries fails the
// primary key as blobfs.ErrIDTaken. When an Ensure finds a row instead of
// inserting one, the found row keeps its own id and the option has no
// effect.
func WithID(id string) CreateOption {
	return func(o *createOptions) {
		o.id = id
		o.hasID = true
	}
}

// VersionOption configures a call that acts on a row the caller read, beyond
// its required arguments: Files.Hold, Files.Delete, Directories.Delete, and
// Directories.MarkDeleting.
type VersionOption func(*holdOptions)

// HoldOption configures one call of Files.Hold beyond its required
// arguments.
type HoldOption = VersionOption

// DeleteOption configures one call of Files.Delete or Directories.Delete
// beyond its required arguments.
type DeleteOption = VersionOption

// holdOptions collects what the version options set.
type holdOptions struct {
	version    int64
	hasVersion bool
}

// AtVersion makes the call act on the row only at version, the value the
// caller read from a listing or an earlier read, so a caller that acts on a
// row it has not read inside its transaction learns that the row moved on:
// a Files.Hold or a Directories.Delete matches nothing, and a Files.Delete
// or a Directories.MarkDeleting leaves the rows as they are, unless the row
// is still at version. A row at another version is
// query.ErrVersionMismatch. A file or directory that is already deleting is
// the delete's or the mark's retry, which converges whatever the version.
func AtVersion(version int64) VersionOption {
	return func(o *holdOptions) {
		o.version = version
		o.hasVersion = true
	}
}
