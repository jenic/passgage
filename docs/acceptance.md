# Platform acceptance

Automated coverage is provided by `make test`, `make test-terminal` (Linux/macOS
PTY regressions, no display needed), `make test-integration` (opt-in
filesystem Git metadata writes), `make test-desktop` (requires a graphical session,
replaces clipboard contents), and `make verify-repro`. Run the following on a
disposable store before adopting a release. Never use real secrets as test fixtures.

## Desktop clipboard

Run on macOS, Windows, Linux/X11, and Linux/Wayland with a supported data-control
compositor. Set `PASSWORD_STORE_CLIP_TIME=3` using the platform's environment syntax.

1. Insert a multiline fixture, then copy its first and second lines. Confirm the
   CLI returns immediately and paste works while it has exited.
2. Wait for expiry and confirm the secret is cleared.
3. Copy another application's text before expiry; verify it remains unchanged.
4. Copy the same fixture twice two seconds apart; the first helper must not clear
   the second copy early. Repeat with different entries.
5. Confirm unsupported primary selection and unavailable displays report errors
   rather than claiming successful copying.
6. Repeat from PowerShell and a Windows path containing spaces. Confirm no extra
   console window appears and the background helper exits after expiry.

## Editing and identities

On Arch Linux with urxvt and zsh, use a translucent background (for example,
an existing RGBA background with alpha `aaaa`). Run `passgage version`, help,
and listings directly, not piped. Confirm no `aaa` or other response fragments
appear at the prompt. Repeat after saving and canceling the built-in editor.
Repeat editor smoke checks in native macOS and Windows terminals. These manual
checks complement, rather than replace, the synthetic PTY regression suite.

Check `PASSGAGE_EDITOR_THEME=dark` and `light` against the corresponding terminal
backgrounds. Empty/unset selects dark. Invalid values must reject built-in editing
without changing the encrypted entry, but must not affect other commands or an
explicit external editor.

1. Open the built-in editor, change multiple Unicode lines, save, and read back.
   Repeat canceling the editor and verify the previous ciphertext is unchanged.
2. Verify terminal state restoration after cancellation and resizing. Verify
   Ctrl+V uses the native clipboard, with no external utilities on PATH.
3. Exercise an explicitly selected external editor, including an executable path
   with spaces. Confirm cancellation/failure preserves the encrypted original.
4. Unlock password-protected age and SSH identities. Reencrypt multiple entries
   and confirm only one key-unlock prompt occurs per invocation.
5. With a real age-plugin-yubikey installation, verify that a command fails without
   consent, works with the allowlist, and handles token removal, wrong PIN, touch
   requests, and cancellation without replacing existing entries.

## Compatibility and synchronization

1. Against copies of a passage store, decrypt shell-written entries in passgage
   and passgage-written entries in the original shell implementation. Include
   multiline Unicode data, trailing newlines, inherited policies, and encrypted keys.
2. Using a disposable SSH/HTTPS remote, clone, commit, push, and pull between two
   copies. Confirm unknown host keys and bad credentials fail without leaking tokens.
3. Stage an unrelated change, mutate an entry, and verify the automatic commit
   excludes that unrelated change while leaving it staged.
4. Diverge two histories. Pull and non-fast-forward push must fail without
   overwriting local entries; recover explicitly with a separate Git client.
5. On Windows, inspect newly created identity and external-editor temporary file
   ACLs: access should be restricted to the current user and SYSTEM.
