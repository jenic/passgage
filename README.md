# passgage

A portable Go implementation of [passage](https://github.com/FiloSottile/passage),
the age-backed password manager. It reads and writes existing passage stores on
macOS, Windows, and Linux. No migration or external `age`, `git`, `ssh`, clipboard,
QR, or Unix utility executable is required for the core workflows.

The permitted child processes are another copy of passgage for clipboard expiry,
an explicitly selected editor, and explicitly allowed age plugins. The default
editor keeps plaintext in memory. Bash extensions are not executed.

## Build and use

Use the Go toolchain pinned in `go.mod` and GNU Make:

```sh
make build
make test
make release-all
make verify-repro
make vuln
```

The local binary is produced in `bin/`; put it on your PATH. Release archives and
SHA-256 checksums are produced in `dist/` for darwin/linux/windows, amd64/arm64.
Desktop builds have `CGO_ENABLED=0`; no C compiler or development libraries are
required. A working graphical session is required only for clipboard operations.
Windows users can run the released executable directly without Make or a shell.

Dependencies are vendored. Make sets `GOCACHE`, `GOMODCACHE`, and `GOPATH` to
project-local directories and runs `go mod verify` before building or testing.
The first build can require downloading the pinned Go toolchain; subsequent
builds use the cache and vendored modules.

For an existing store, use your existing environment variables and commands:

```sh
passgage ls
passgage mail/work
passgage show -c mail/work
passgage show --clip=2 mail/work
passgage edit mail/work
passgage generate -n -i mail/work 30
```

For a new store:

```sh
passgage init --passphrase
passgage insert mail/work
passgage generate service/account
```

`init` creates a native X25519 identity and a root recipients file. Without
`--passphrase`, the private identity is stored unencrypted with restricted file
permissions. Initialization never replaces an existing identity or recipients
file. Hardware-token provisioning remains the plugin's responsibility.

## Commands

| Command | Behavior |
| --- | --- |
| `[show] [entry\|directory]`, `ls`, `list` | Print exact decrypted bytes, or a sorted entry tree |
| `find term...`, `search` | Case-insensitive substring search of names |
| `grep [flags] pattern` | Search decrypted lines using Go regular expressions |
| `insert`, `add` | Hidden confirmed input; `-e` visible single line; `-m` complete stdin |
| `edit entry` | In-memory terminal editor; Ctrl+S saves, Esc/Ctrl+C cancels |
| `generate entry [length]` | Cryptographically random password; default length 25 |
| `reencrypt [-p directory]` | Apply current recipient policies to existing entries |
| `rm`, `remove`, `delete` | Remove entries; `-r` includes a directory's entries and policies |
| `cp`, `copy`; `mv`, `move`, `rename` | Copy/move entries and re-encrypt for destination policies |
| `git command` | Native Git subset described below |
| `completion shell` | Bash, Zsh, Fish, or PowerShell completion |
| `help`, `version`, `--version` | Usage and build version; no keys required |

Bare `passgage`, `show`, `ls`, and `list` display a native tree without requiring
the `tree` command:

```text
Passgage
├── email
│   ├── personal
│   └── work
└── infrastructure
    └── server
```

`show email` uses `email` as the heading and lists its descendants. Listings
include only encrypted entry names and their parent directories, not hidden
metadata, symlinks, ordinary files, or empty directories. An empty listing prints
only its heading. Siblings use case-sensitive, locale-independent sorting.
Directories are bold blue on supported terminals; redirected output,
`TERM=dumb`, nonempty `NO_COLOR`, and `CLICOLOR=0` disable color. Forced-color
settings do not override this policy. Unicode branches remain when piped.
This replaces the previous flat listing format; `find`/`search` retain flat output.

`show -c[LINE]` / `--clip[=LINE]` copies a line; `-q[LINE]` /
`--qrcode[=LINE]` renders it as a terminal QR code. The default line is 1.
For `generate`, `-c` and `-q` select clipboard or QR output; `-n` excludes symbols
and `-i` replaces only the first line, preserving the remainder exactly.

Use `--force` to allow overwrites or deletion without an interactive prompt.
Noninteractive destructive operations otherwise fail. Copy/move destination
collisions require `--force`; they do not open an overwrite prompt.
Directory copies preserve nested `.age-recipients` policies. Unrelated files and
other hidden metadata are not copied/deleted. Empty directories are pruned.

Grep supports `-i`, `-n`, `-l`, `-v`, `-F`, and `-E`; `-E` is the default.
It uses Go's regular-expression syntax, not GNU grep's full option/regex language.
Output is `entry:line` or `entry:line-number:line`; no matches returns exit code 1.

To explicitly use an external editor:

```sh
passgage edit --external code --editor-arg=--wait mail/work
```

The executable and each argument are passed directly, without shell parsing.
`EDITOR` and `VISUAL` do not implicitly launch programs. External editing uses a
restricted temporary plaintext file, which is removed afterward; the original
encrypted entry is preserved if the editor exits unsuccessfully or encryption
fails. Default editing does not create that file.

The built-in editor uses Bubble Tea v2 with explicit dark styling by default.
Set `PASSGAGE_EDITOR_THEME=light` for light-background styling, or `dark` to
select the default explicitly. Empty values also select dark; other values
are rejected only when opening the built-in editor. External editing ignores
this setting. The editor does not query foreground/background colors, avoiding
the urxvt RGBA-response leakage that could leave `aaa` at the shell prompt.
Normal commands do not interrogate the terminal on editor-package import;
the running editor may still negotiate other terminal capabilities.

## Configuration and age plugins

| Variable | Default / purpose |
| --- | --- |
| `PASSAGE_DIR` | User home + `.passage/store` |
| `PASSAGE_IDENTITIES_FILE` | User home + `.passage/identities` |
| `PASSAGE_RECIPIENTS_FILE` | Explicit recipient file; highest encryption precedence |
| `PASSAGE_RECIPIENTS` | Whitespace-separated recipients |
| `PASSWORD_STORE_GENERATED_LENGTH` | `25`, range 1–1048576 |
| `PASSWORD_STORE_CHARACTER_SET` | `[:punct:][:alnum:]` |
| `PASSWORD_STORE_CHARACTER_SET_NO_SYMBOLS` | `[:alnum:]` |
| `PASSWORD_STORE_CLIP_TIME` | `45` seconds, range 1–86400 |
| `PASSWORD_STORE_X_SELECTION` | `clipboard`; `primary` on supported Linux sessions |
| `PASSGAGE_AGE_PLUGINS` | Comma-separated allowlist of plugin names |
| `PASSGAGE_EDITOR_THEME` | Built-in editor styling: `dark` (default) or `light`; no color queries |
| `PASSGAGE_CACHE_DIR` | Clipboard coordination directory; defaults to OS user cache + `passgage` |

After explicit recipient overrides, encryption searches from the entry's parent
up to the store root for `.age-recipients`. Without any policy file it derives
recipients from local identities. An empty policy is an error, not permission to
fall back to another key. Standard age identities, password-encrypted identity
files, and SSH RSA/Ed25519 identities are supported. Identity unlocks are cached
only within the current command; passphrases require a terminal.

Plugin identities and recipients require explicit consent:

```sh
passgage --allow-age-plugin=yubikey show mail/work
```

The corresponding `age-plugin-yubikey` must be installed on PATH. The official
age plugin protocol handles token/PIN interaction. Listing, help, and completion
do not require unlocking identities.

`PASSAGE_AGE` is rejected for cryptographic operations. Bash extensions and their
settings are unsupported. `PASSWORD_STORE_UMASK` does not relax permissions:
new private files always use mode 0600 on Unix and a user/SYSTEM-only ACL on
Windows. Paths use `/` separators on every OS; traversal, hidden metadata paths,
symlink entries, and Windows-reserved names are rejected.

## Clipboard lifetime

The foreground command returns after a background passgage helper confirms that
the secret has been copied. Secret bytes travel through an anonymous pipe, not
command arguments, environment variables, or disk. At expiry the helper checks
ownership, a unique clipboard marker, and content before clearing; newer clipboard
content is left alone. It clears rather than restoring the previous clipboard.

macOS and Windows use native APIs. Linux uses native Wayland data-control when
available, otherwise X11/XWayland. Unsupported compositors and headless sessions
return errors, with no fallback to `wl-copy`, `xclip`, or OSC52. Primary selection
is unsupported on macOS and Windows. Clipboard-history managers may retain copies;
passgage cannot erase other applications' history. Stopping the helper prematurely
can prevent expiry cleanup.

## Git history and synchronization

When the store itself is a Git repository, successful mutations automatically
commit affected paths. Unrelated staging is preserved. Before committing, stderr
includes the custom passage diagnostic:

```text
Committing <directory>: <message>
```

If the encrypted file was saved but committing failed, the command says so and
returns failure; the saved secret is not silently discarded. Configure an author
using your existing Git configuration, or `passgage git config user.name` and
`passgage git config user.email` with values. `git init` creates an initial commit;
if identity configuration is missing, configure it, then explicitly add/commit.

Supported syntax:

```text
git init
git clone <https-or-ssh-url>
git status
git log
git diff [--decrypt]
git add <path>...
git commit -m <message>
git config <user.name|user.email> [value]
git remote [add <name> <url>|remove <name>|set-url <name> <url>]
git fetch [remote]
git pull [remote]
git push [remote]
```

Paths to `git add` are physical store-relative paths, including `.age` suffixes.
`git diff` summarizes changes against HEAD; `--decrypt` explicitly displays full
before/after plaintext for changed encrypted entries. It never installs a Git
textconv command. The supported commands are a subset, not arbitrary Git passthrough.

Pull requires a clean working tree/index and fast-forwards only. Diverged history
and conflicting changes are rejected; resolve them separately with your Git client.
Push does not force updates. Fetch/pull/push default to `origin`, run only when
requested, and use a two-minute transport deadline.

HTTPS uses `PASSGAGE_GIT_TOKEN` and optional `PASSGAGE_GIT_USER` (default `git`).
Authenticated clone/fetch/pull/push can prompt for a token when one is not configured.
Tokens are not saved to configuration. SSH supports existing agents, private keys,
known-host verification, and common host aliases. `PASSGAGE_GIT_SSH_KEY` selects an
explicit key; otherwise agent/configured/default keys are considered. Unknown or
changed host keys fail verification; manage known_hosts separately.

Local-file, insecure HTTP, and git-protocol remotes are disabled. Credential
helpers, hooks, external filters/signing, SSH ProxyCommand/ProxyJump, and arbitrary
SSH command overrides are not executed. Required signing and configured filters
cause errors instead of silent unsigned/unfiltered commits. Nested repositories,
submodules, Git worktree management, merges, and rebases are outside this subset.

## Verification and limits

`make test` uses isolated secret fixtures, an executable test age plugin, mocked
clipboard lifecycle tests, and in-memory Git repositories/transports. It does not
write repository Git metadata. `make test-integration` explicitly enables disposable
filesystem Git tests and is intended for CI or a user-controlled environment.
`make test-reference` compares both encryption directions with the original shell
script in disposable stores; set `PASSGAGE_REFERENCE_SCRIPT` to that script and
`PASSGAGE_REFERENCE_AGE` to an official age executable. Those programs are used
only by this optional compatibility test, never by passgage itself.
CI runs tests on Linux, macOS, and Windows and checks cross-build reproducibility.
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
token was exercised. This implementation has not undergone an independent audit.

Writes replace complete ciphertext files; failed decryption/encryption preserves
original entries. Directory operations are atomic per file, not across an entire
batch. Errors report partial changes; failed batches are not auto-committed.
The store lock coordinates passgage writers, not the original shell script or
other Git clients. Avoid concurrent mutations through those other programs.

## Attribution and license

GPL-2.0-or-later; see [COPYING](COPYING). Based on passage by Filippo Valsorda and
password-store by Jason A. Donenfeld. The reference shell implementation carries
Copyright (C) 2012–2018 Jason A. Donenfeld, all rights reserved. This implementation
also preserves Jenic Rycr's custom verbose-commit behavior. Vendored dependencies
retain their own license notices.
