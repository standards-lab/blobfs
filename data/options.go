package data

// Option configures New beyond its required arguments.
type Option func(*options)

// options collects what the options set.
type options struct {
	variant Variant
}

// WithVariant makes the store forward its variation points to v instead
// of Standard. The variant is built by its own constructor against the
// same catalog and dialect, so a consumer composes an engine's variant and
// then New(catalog, dialect, WithVariant(v)), or passes an implementation
// of its own.
func WithVariant(v Variant) Option {
	return func(o *options) { o.variant = v }
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

// HoldOption configures one call of Files.Hold beyond its required
// arguments.
type HoldOption func(*holdOptions)

// holdOptions collects what the hold options set.
type holdOptions struct {
	version    int64
	hasVersion bool
}

// AtVersion makes Files.Hold match the row only at version, the value the
// caller read from a listing or an earlier read, so a caller that acts on a
// row it has not read inside its transaction learns that the row moved on.
// A row at another version is query.ErrVersionMismatch.
func AtVersion(version int64) HoldOption {
	return func(o *holdOptions) {
		o.version = version
		o.hasVersion = true
	}
}
