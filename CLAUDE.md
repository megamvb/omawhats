# CLAUDE.md — working on OmaWhats

Notes for an AI assistant working in this repository. The README is the user's
documentation; this file is the part that is not obvious from the code, plus
the house rules. Read both.

**Talk to Marcos in Portuguese. Everything the project itself says — UI, daemon
errors, notifications, README, code comments, commit messages — stays English.**

## What this is

OmaWhats is a WhatsApp client for the Omarchy shell: a Go daemon (`omawhatsd`,
built on [whatsmeow](https://github.com/tulir/whatsmeow)) that holds the session
and a QML plugin (`megamvb.omawhats`) that draws the bar widget and the client.
They talk over a unix socket, one JSON object per line (protocol listed at the
end of the README).

**The repository root *is* the plugin folder.** `omarchy plugin add <url>` clones
it straight into `~/.config/omarchy/plugins/megamvb.omawhats/`, so `manifest.json`
and the QML files sit at the top, the daemon is in `daemon/`, the `Model.js` tests
in `tests/`. Consequences: `omarchy-plugin-validate` must pass on this folder,
and **no symlinks anywhere in it** (the validator refuses them, `.git` excepted).

## Commands

```bash
make test       # go vet, the daemon tests, then the Model.js tests (node tests/run.js)
make validate   # what `omarchy plugin add` runs on this folder
make install    # build + binary into ~/.local/bin + plugin files into the plugin folder
./install-daemon  # what a user runs after cloning: checks Go >= 1.27 (or mise), builds, installs
```

Run `make test` before every commit. `make install` is how a development machine
updates itself; a machine that installed from the git URL uses
`omarchy plugin update megamvb.omawhats` + `install-daemon`.

Useful while debugging: `omawhats open|quit|status` (the whole client on or off),
`omawhatsd status|snapshot|history <jid>`, `omarchy-shell megamvb.omawhats debug`
(the widget's last 30 trace lines), `~/.local/state/omawhats/daemon.log`.

## House rules — the account is real

Marcos's own WhatsApp is paired to this daemon. These are not suggestions:

- **Never read or screenshot the real conversations.** For anything visual, build
  a harness with invented chats and generated pictures (see Testing), or point
  the widget's `binary` setting at a wrapper exporting `OMAWHATS_DATA=<fake dir>`.
- **Never send a message, a file or a reaction from his account without asking** —
  there is a real person on the other side.
- **Never test `logout`** on the real account; use a fake data dir.
- No systemd, ever. The daemon runs only between an explicit start and stop —
  that is Marcos's deliberate choice, not an oversight.
- Harness copies keep `AudioOutput { volume: 0 }`, and stub anything that reaches
  the outside world: `openLink()` really opens the browser, `omarchy-file-select`
  really opens the portal chooser, `wl-paste` really reads the clipboard.
- **Commit and push only when asked.**

## Conventions

- Commit messages: `area: a lowercase sentence in plain words` (`media: a 403 is
  the phone's cue too, not the end of the road`), then a prose body saying what
  was wrong and why this is the fix — not a list of files. End with
  `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- **A version bump touches two files**: `version` in `manifest.json` and `const
  version` in `daemon/paths.go`. They must match — the client shows both, so a
  stale daemon is visible at a glance, and `tests/run.js` asserts the string.
- Prose in comments and messages explains *why*, in whole sentences. Match it.
- Anything worth a real test belongs in `Model.js`, not in a QML binding.

## Git and the other machine

`omarchy plugin update` **only fast-forwards**; it rolls back with
`git reset --hard ORIG_HEAD`. So: never rewrite pushed history (no squash, no
force-push) without warning — the other computer's clone would stop updating and
need a manual `git reset --hard origin/main`. Never edit the installed clone
in place either; edit here and push.

Remote: `https://github.com/megamvb/omawhats.git`, branch `main`.

## Testing

- `daemon/*_test.go` — Go, plain `go test`. Prefer extracting a pure function
  (`fetchStem`, `notifyArgs`, `refusedByWhatsApp`) over polling goroutine state.
- `tests/run.js` — node, asserts over `Model.js`. This is where the honest tests
  live, because `Model.js` is loadable outside Quickshell.
- QML harness: `QT_FORCE_STDERR_LOGGING=1 QT_QPA_PLATFORM=offscreen qml6 X.qml`.
  Without the first variable `console.log` goes to the journal, not the terminal.
  Add `QT_QUICK_BACKEND=software` for `grabToImage`. Positioner and Layout
  geometry is only valid after a frame — read it from a `Timer`, never from
  `Component.onCompleted`.
- A full fake shell: a throwaway quickshell config dir with `Commons` and `Ui`
  symlinked to `/usr/share/omarchy/shell/*`, copies of the plugin files, a
  `shell.qml` driving Chat, and `OMAWHATS_DATA|SOCKET|CACHE` plus `XDG_STATE_HOME`
  pointed at a test dir (otherwise the real `daemon.log` gets rotated).
- Verify a fix by reproducing the failure first. A test that passes before the
  fix proves nothing; say so when that is what happened.

## Gotchas that have cost hours

- **`Image.sourceSize` announces a change only against the size the Image held
  before the new source started loading.** Two pictures of the same dimensions in
  a row never re-announce, so a binding stuck at 0 lays the item out 0x0 —
  invisible, `status === Image.Ready`, no warning. Read the size imperatively on
  `statusChanged`/`sourceChanged` instead of binding to it.
- With `cache: true` a re-used file path serves the **old** pixmap even after the
  bytes on disk changed. A forced re-download must land under a new file name.
- `anchors.bottom: cond ? x : undefined` does not give an item its place back.
  Compute `y:` instead.
- Quickshell `Socket` does not retry, and flipping `connected` false→true in one
  tick is a no-op: each attempt is a new Socket via `createObject`, created
  disconnected and assigned *before* connecting.
- The daemon writes `<socket>.pid` once it is listening and removes it on the way
  out; the plugin watches that directory with `FolderListModel`. That is how a
  start or a stop is noticed without polling.
- Read `StdioCollector.text` in `onStreamFinished`, not `onExited`.
- A QML `Item` already has `focus` and `state` — Service uses `focusOn()` and
  `daemonState`.
- Plugin `console.log` never reaches `qs log`; use the `debug` IPC target.
- After a hot-reload the shell's IPC can stay bound to the stale instance
  ("Function not found"): `omarchy restart shell`.
- whatsmeow's `Store.Contacts`/`LIDs` are nil on an unpaired device — guard with
  `storeReady()` or the daemon panics.
- A media download refused with 403/404/410 is returned by whatsmeow without
  trying another host: the phone is the only place left to ask
  (`SendMediaRetryReceipt`). 403 means the stored address is simply too old.
- SQLite here is **not** a bottleneck (measured: `listChats(200)` 2.5 ms,
  `history(80)` 2.9 ms on a 1189-chat DB). Loose writes cost ~10x a transaction;
  that is the thing to look at.

## Where the data lives

| What | Where | Override |
|---|---|---|
| Session + history (`whatsapp.db`), pins, lock | `~/.local/share/omawhats/` | `OMAWHATS_DATA` |
| Downloaded media and thumbnails | `~/.cache/omawhats/media/` | `OMAWHATS_CACHE` |
| Socket (+ `.pid` marker) | `$XDG_RUNTIME_DIR/omawhats.sock` | `OMAWHATS_SOCKET` |
| Daemon log, saved bar entry | `~/.local/state/omawhats/` | `XDG_STATE_HOME` |

Every one of those comes from a single function (`daemon/paths.go`,
`daemon/media.go`, and the pure mirrors `Model.dataDir/socketPath/cacheDir`), so
pointing a test — or one day a second account — somewhere else is a small change.
