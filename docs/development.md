# Development

The reasoning behind the repository's configuration: the module list, the check, the SQL
conventions, and the development stack. The [README](../README.md#development) lists the tasks.

## Modules

`GO_MODULES` in `mise.toml` lists the module directories, kept in sync with the `go.work`
use-list: `.` is the base module, `postgres` the engine sub-module, and `example` the
composition proof. The maintenance tasks skip a directory without a `go.mod`; `check`,
`currency`, and `upgrade` require one. `upgrade` sets each `go` directive to the current Go
minor, with no `toolchain` line.

## The check

The check is read-only and deterministic, and stops at the first failure. It builds, vets,
formats, fixes, tidies, tests, and lints each module with the workspace off, so each resolves
its own pins. sqlint then runs with the workspace on: it resolves the modules `sqlint.toml`
names with `go list -m`, which the base module reaches only through the committed `go.work`.
`go list -m` does not download, so the check fetches the engine module first; the `lint` task
does the same.

## SQL conventions

`sqlint.toml` holds the repository's SQL conventions, one file at the root for the base module
and the engine sub-module alike. `engine` names the native forms a standard-tier file may not
use. `[sources]` names the pattern catalogs the statements' includes resolve against: sqlate's
own patterns and the namespace blobfs publishes, a bare directory of pattern files. `[export]`
is what a consumer reads when it names blobfs as a source: the directory its published patterns
live in, registered under the consumer's chosen namespace.

The statements in `data/statements` and `postgres/statements` are named for their operation,
keep the delimiter out of comments and literals, use no native form in a standard-tier file,
and carry the guard protocol's two includes together. The native-forms check keys on each
file's declared tier, so `postgres/statements` keeps it on: a native file there is exempt by
its tier and must carry its port note to compile, and a file there that declared the standard
tier is still held to the standard forms. The published patterns take the same delimiter and
native-form checks. The migrations role has no native-forms check, since the DDL is
PostgreSQL's, the only engine's; each migration holds a single statement.

## The development stack

`compose.yml` runs PostgreSQL for the metadata and Azurite for the bytes, which the integration
tier and the example run against. Each service builds from `compose/<service>/Dockerfile`,
whose `FROM` line is the service's one pin and which carries its configuration and health
check. Named volumes keep the data across `mise run db:down`; `mise run db:reset` drops them.

- **Ports.** PostgreSQL listens on 5434 by default, so the stack coexists with sqlate's on 5433
  and a service's on 5432; Azurite's blob service listens on 10000. `POSTGRES_PORT` and
  `AZURITE_BLOB_PORT` move them, and the connection settings in `mise.toml`'s `[env]` follow
  the same two variables.
- **Credentials.** The PostgreSQL user, password, and database `app` are development values,
  and the Azurite account and key are the emulator's published development credentials; none
  is a secret.
- **PostgreSQL's health check.** It is healthy once the server accepts TCP connections to the
  `app` database. The probe goes over TCP, not the socket, because the first start's
  initialization runs a temporary server on the socket alone, which would report ready before
  the real server starts.
- **Azurite's health check.** The image defines none and has no curl; its busybox `nc` shows
  the blob listener accepts connections.
- **Azurite's API version check.** The Azure SDK sends an `x-ms-version` that Azurite rejects
  unless the version check is off, so the service runs with `--skipApiVersionCheck`. Its data
  lives in `/data`, the stack's named volume.
