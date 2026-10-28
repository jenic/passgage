# Development

Build, test, and release instructions for passgage contributors and maintainers.
See [README.md](README.md) for installation, usage, compatibility, and
[Sigstore download verification](README.md#verifying-downloads).

## Building and installing from source

Use the Go toolchain pinned in `go.mod` and GNU Make:

```sh
make build
make release-all
make verify-repro
```

The local binary is produced in `bin/`; put it on your PATH. Release archives and
SHA-256 checksums are produced in `dist/` for darwin/linux/windows, amd64/arm64.
Desktop builds have `CGO_ENABLED=0`; no C compiler or development libraries are
required.

Run `make install` to build and install the binary into `/usr/local/bin` on
Linux and macOS, or `%LOCALAPPDATA%/Programs/passgage` on Windows. Override the
destination with `make install BINDIR="/path/to/bin"`. On Linux and macOS, the
target automatically uses `sudo` for the installation commands when the
destination requires elevated permissions. Run `make install` as your normal
user so the build and Go caches remain owned by you. Writable destinations,
such as `make install BINDIR="$HOME/.local/bin"`, do not require `sudo`.
On Windows, run Make from a POSIX environment such as Git Bash with GNU Make
and the `install` utility available. Add the installation directory to your
user PATH if it is not already included.

Dependencies are vendored. Make sets `GOCACHE`, `GOMODCACHE`, and `GOPATH` to
project-local directories and runs `go mod verify` before building or testing.
The first build can require downloading the pinned Go toolchain; subsequent
builds use the cache and vendored modules.

## Library boundaries

Public `store` owns passage store operations and mutation coordination; public
`agecrypto` owns encryption, identity loading, and plugin interaction. Shared
rooted writes and platform permission enforcement live in `internal/storefs`.
CLI configuration, rendering, Git, clipboard, and editors stay internal. The CLI
uses the same public library API as external consumers. Keep one implementation
and one root module; public API changes ship with the existing release tags.

The library accepts explicit configuration and caller-controlled interaction.
Avoid adding environment defaults, terminal output, Git actions, or new subprocesses
to store workflows. Keep lazy identity loading so listing and completion work
without keys. Crypto engines cache identities for their lifetime and serialize
operations; stores serialize operations and automatically lock mutations.

## Testing and verification

Run the standard test suite and dependency vulnerability scan with:

```sh
make test
make vuln
```

`make test` uses isolated secret fixtures, an executable test age plugin, mocked
clipboard lifecycle tests, and in-memory Git repositories/transports. It does not
write repository Git metadata. The default suite also builds a disposable external
Go module against the public API and checks its import dependencies. It uses
repository-local caches, verifies modules before vendoring, and disables networking;
it does not use dependency packages' test suites. Existing library examples execute
as part of the default tests. For concurrent library operations, run
`make test CGO_ENABLED=1 TEST_FLAGS='-race'` with a C compiler installed.
`make test-integration` explicitly enables disposable
filesystem Git tests and is intended for CI or a user-controlled environment.
`make test-reference` compares both encryption directions with the original shell
script in disposable stores; set `PASSGAGE_REFERENCE_SCRIPT` to that script and
`PASSGAGE_REFERENCE_AGE` to an official age executable. Those programs are used
only by this optional compatibility test, never by passgage itself.
CI runs tests on Linux, macOS, and Windows and checks cross-build reproducibility.
`make test-completion` builds the binary and exercises its generated completion
scripts in actual shells. It requires Bash plus `bash-completion`, Zsh, Fish, and
`pwsh` by default. Select a subset with, for example,
`PASSGAGE_COMPLETION_SHELLS=bash,zsh,fish make test-completion`; a missing requested
shell fails the test. `PASSGAGE_BASH_COMPLETION` can override the Bash library path.
CI runs Bash/Zsh/Fish terminal or completion-API checks on Linux and PowerShell
checks on Windows using PowerShell 7 with PSReadLine, with isolated synthetic
stores and no decryption keys.
`make test-terminal` runs the actual executable under synthetic Unix terminals
on Linux and macOS. It checks query-free ordinary commands, editor save/cancel,
terminal restoration, and unread response bytes without requiring a display.
The simulator includes urxvt-style RGBA replies; no TTY drain is added to runtime.

`make test-desktop` exercises the actual executable's clipboard helper; it needs
a graphical session and temporarily replaces clipboard contents. Linux CI uses Xvfb.

The dependency scan reports no reachable vulnerabilities with the pinned versions.
Its [module-level advisory](https://pkg.go.dev/vuln/GO-2026-5932) for deprecated `golang.org/x/crypto/openpgp` does not
affect imported packages or called code; passgage does not use OpenPGP encryption.

See [platform acceptance checks](docs/acceptance.md) for real desktop and hardware
tests. Cross-compilation does not establish that a desktop clipboard or a physical
token was exercised.

`make test-release` runs offline release automation tests using Python 3 and Bash.
The tests simulate Git, git-cliff, Go, GitHub, and Cosign; they do not create real
tags, contact the Go proxy, publish releases, or contact Sigstore. Full OIDC signing
and publication are validated with a maintainer-created prerelease tag after
merging the workflow.

## Publishing and releases

Pushing a `v`-prefixed SemVer tag, such as `v1.2.3` or `v1.2.3-rc.1`, starts
the release workflow. The tag must include the release workflow changes. A
maintainer creates and pushes the tag; the workflow does not create or move tags.
Local publishing and the workflow both require a publishable module path in
`go.mod` and a matching Go module version. The current path supports v0 and v1;
v2 and later require a matching `/vN` suffix and updated imports. Tags with
`+build` metadata (including `+incompatible`) are rejected.
Prerelease tags produce GitHub prereleases and are not marked as Latest.

Use `make publish-check VERSION=v1.2.3` to validate the tag and publishing
prerequisites without running tests or creating a tag. This requires Bash, Git,
Go, and `git-cliff`, a clean worktree (including no untracked files), and an `origin`
remote. The tag must be a valid `v`-prefixed SemVer and must not already exist
locally or on `origin`. Checking remote tags requires network access.

Run `make publish VERSION=v1.2.3` to run those checks, `make module-check`,
`make test`, and `make test-release`. The module check runs `go mod verify` and
`go mod tidy -diff`; it reports required changes without rewriting `go.mod` or
`go.sum` and can require network access for dependencies absent from the cache.
Resolve and commit any dependency changes and regenerate vendor before retrying.
Publishing then stops with instructions to opt in. After committing
all release changes, including `cliff.toml`, publish with:

```sh
make publish VERSION=v1.2.3 PUBLISH=1
```

This repeats the checks and tests, generates notes using
`git-cliff --unreleased --tag v1.2.3`, creates a signed annotated tag with those
notes, and pushes only that tag to `origin`. It then requests Go proxy indexing
using `GOPROXY=proxy.golang.org go list -mod=readonly -m <module>@<tag>`, with the
pinned toolchain and local caches. Configure Git tag signing and your
signing key first. A failed notes-generation or signing step stops before the
push. If pushing fails after the tag is created, inspect the local tag and remote
state before retrying the push manually; publishing refuses to replace tags.
GitHub Actions builds and publishes the binaries after the tag is pushed.

If indexing fails, the tag remains pushed and GitHub Actions can continue.
After resolving the proxy error or allowing time for the tag to become visible,
retry only the lookup with `make index-module VERSION=v1.2.3`. This command
validates the module/version but does not require a clean worktree, run tests,
create tags, or push anything. For tags pushed manually, run this command to
request indexing; the release workflow does not request it automatically.

Publication waits for the full existing CI suite, including Linux, macOS, and
Windows checks and cross-build reproducibility. The workflow then builds the
tagged commit with `make release-all VERSION=<tag>`, preserving the full tag in
`passgage version`. Fresh runners download the pinned modules, verify them and
check dependency tidiness with `make module-check`, generate the ignored vendor
directory, and build with `-mod=vendor`.

Signing uses Cosign v3.0.6, GitHub Actions OIDC, and the public Sigstore services.
No signing-key secret is needed. Repository Actions policy must allow the pinned
actions and permit the publication job's `contents: write` and `id-token: write`
permissions. The runner needs access to GitHub, Go module downloads, and Sigstore.
The signing identity and artifact digests are recorded in public transparency logs.

All signatures are verified before upload. Assets are attached to a draft,
downloaded and verified again, and only then published with generated release
notes. This ordering also supports repositories with immutable releases enabled.
An upload or verification failure leaves the release unpublished; use GitHub
Actions' rerun controls to retry. Reruns replace the expected assets on an existing
draft. Unexpected assets block publication and need maintainer review. An already
published release is never intentionally modified; corrections use a new version
tag. The workflow also refuses publication if the remote tag has moved away from
the triggering commit.
