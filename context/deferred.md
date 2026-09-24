# Deferred, with triggers

Capabilities the library leaves out on purpose, each with what would bring it in. The library's
own navigation is one directory at a time; everything here either crosses directories, adds a
policy a consumer may not want, or waits for a second consumer to show its shape.

- **Subtree search.** A search that descends a subtree, with its own cost profile. No trigger is
  named yet.
- **Directory copy.** A file copy is a consumer's composition over the protocols; a directory copy
  is the same walk as subtree search and is deferred with it.
- **A lookup by storage key.** A reconciler that lists the store's keys and asks which rows they
  belong to. Its cost is a new unique index on `blobfs_file (key)`. Trigger: `v1.messaging`.
- **The sweeper.** Finds pending rows an abandoned write left and deleting rows a stopped delete
  left, and finishes them; the file listing's status filter is what it reads. A candidate command
  beside the library. Trigger: `v1.messaging`.
- **A directory rename that skips the tree lock.** A rename cannot form a cycle, so it needs no
  lock; unmeasured, and worth adding only for a consumer that renames directories at volume.
- **A serializing standard-tier variant.** A root-row update held to commit would serialize moves
  on an engine without an advisory lock, at the cost of writing the root on every move. Trigger: a
  consumer on such an engine.
- **A consumer-supplied name validator,** narrower than the library's rules. Trigger: a consumer
  that asks.
- **Soft delete and a recycle bin.** Trigger: the first domain that needs the convention
  `go-web-service`'s data layer defers.
- **Content replacement and versioning.** Trigger: a consumer that overwrites a file's content.
- **File checksums.** `go-storage` exposes none, so the consumer would compute one while
  streaming. Trigger: a consumer that verifies content.
- **A checksum for migration text** beyond the golden test. Trigger: a released migration changed
  in place that the golden test missed.
- **A `blobfstest` toolkit** beyond the conformance suite. Trigger: what the first consumer's tests
  need.
- **An interleaved directory-and-file listing.** Trigger: a consumer that builds a folder browser.
- **MySQL and MariaDB.** A second engine sub-module, with its DDL and native forms; its port notes
  are the work list. Trigger: the second SQL engine (`backlog.second-providers` in the workspace
  roadmap).

## Assumptions

- The baseline runs on any engine `sqlate` has a dialect for, but only PostgreSQL is exercised.
- The key rule of a real Azure account matches `go-storage`'s azureblob validator; the example ran
  against Azurite, which accepts keys the real service refuses.
- The consumer-side ownership composition, an owner row per top-level directory or a join row per
  file, generalizes to a second domain once one exists.
- No concurrency beyond the suite's paired transactions and the listing stress in `sqlate` was
  measured; plan shapes and buffer counts carry across machines, timings do not.
