# Repository guidelines

Passgage is a native Go implementation of passage. Keep command parsing in
`internal/cli`, filesystem and encryption logic in `internal/store`, and platform
integrations in dedicated internal packages. The entry point is `cmd/passgage`.

Use `make build`, `make test`, and `make release-all`. All Go commands must set
GOCACHE and GOMODCACHE to this repository's `.gocache` and `.gomodcache`.
Dependencies are vendored; verify modules before builds and tests and use
`-mod=vendor`. Pin the toolchain in go.mod and CI.

Use gofmt, standard testing, isolated fixtures, and Conventional Commits.
Document exported declarations. Keep secrets out of logs and process arguments.
Never add subprocesses to core workflows. The only permitted subprocesses are
the clipboard helper, explicitly selected editors, and explicitly allowed age
plugins. Tests must not modify repository Git metadata; local Git tests use
in-memory storage. Real filesystem Git integration tests are opt-in.

Keep README.md current, including compatibility differences and platform limits.
Build releases with trimpath, no embedded timestamps, and an empty build ID.
