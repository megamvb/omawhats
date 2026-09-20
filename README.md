# OmaWhats (megamvb.omawhats)

OmaWhats is a basic WhatsApp client in the Omarchy shell, built on
[whatsmeow](https://github.com/tulir/whatsmeow). Two parts:

- **`omawhatsd`** (Go): holds the "linked device" session, stores
  messages in SQLite and speaks JSON lines on a unix socket.
- **`megamvb.omawhats` plugin** (QML): bar icon with the unread count, a panel
  with the power switch, pairing QR and recent chats, and a full-screen client.

![The client: the chat list on the left, a conversation with a photo, a reply,
a voice message and a link on the right](preview.png)

*(made-up conversation — the theme is whatever Omarchy theme you are using)*

> **whatsmeow is an unofficial client, and WhatsApp may suspend accounts that
> use one.** For light personal use the risk is low, but it is yours to take.
> Read [Warning](#warning) before you link your account.

## Install

```bash
omarchy plugin add https://github.com/megamvb/omawhats.git --enable
~/.config/omarchy/plugins/megamvb.omawhats/install-daemon
```

The first command clones this repository into
`~/.config/omarchy/plugins/megamvb.omawhats/` and puts the widget on the bar.
The second builds `omawhatsd` into `~/.local/bin` — the plugin flow compiles
nothing, so this step is not optional. It needs **Go 1.27+**: the system's
(`sudo pacman -S go`), or, if you would rather not install one, it offers to
borrow one through **mise**, which Omarchy already ships (`--mise` forces that,
`mise use -g go@latest` keeps it). Anything else missing is named, not demanded.

Then middle-click the icon to turn OmaWhats on and scan the QR under
*WhatsApp → Settings → Linked devices → Link a device*.

To update:

```bash
omarchy plugin update megamvb.omawhats
~/.config/omarchy/plugins/megamvb.omawhats/install-daemon
```

and turn the daemon off and on once, so the new binary is the one running.

### Requirements

Omarchy (the shell plugin API, `omarchy-file-select` and
`omarchy-launch-browser`) and, to build, Go 1.27+ from pacman or from mise.
Everything else is optional and only costs you the feature next to it:

| Package | Without it |
|---|---|
| `ffmpeg` | animated GIFs show a still frame |
| `wl-clipboard` | Ctrl+V cannot attach a screenshot, and copying a message does nothing |
| `libnotify` | no notifications for new messages |
| `qt6-multimedia-ffmpeg` | voice messages do not play in the client |
| `mpv` | videos open in whatever `xdg-open` picks |

## Not a service

The daemon runs only when you turn it on and stops when you turn it off. No
systemd.

| How | Turn on | Turn off |
|---|---|---|
| Bar | middle-click the icon, or ⏻ in the panel | same |
| Client | Ctrl+Shift+P | same |
| Terminal | `omawhatsd start` | `omawhatsd stop` |
| IPC | `omarchy-shell megamvb.omawhats start` | `omarchy-shell megamvb.omawhats stop` |

Turned off, the panel and the client still open and show what was stored
(read-only). A daemon started from a terminal is picked up by the plugin
immediately.

### Away entirely, and back on demand

Turning the daemon off leaves the widget in the bar. To leave nothing of
OmaWhats running or on screen, and call it up only when you want it, use the
`omawhats` command that `install-daemon` puts next to the daemon:

```bash
omawhats quit     # closes the client, stops the daemon, takes the widget off the bar
omawhats open     # widget back where it was, daemon on, client open
omawhats window   # the same, with the client in a window
omawhats toggle   # quit if anything is on, open otherwise
omawhats status   # what is on
```

`quit` remembers where the widget sat and the settings its bar entry carried
(disabling a widget drops that entry), so `open` puts it back in the same
place, with the same settings, rather than at the end of the section.

The installer also adds a launcher entry, so **Apps → OmaWhats** in the
Omarchy menu opens it. For rows in the menu itself, add these to
`~/.config/omarchy/extensions/omarchy-menu.jsonc`:

```jsonc
"whatsapp": {"icon":"","label":"WhatsApp","aliases":["whatsapp","omawhats"],"description":"Open the OmaWhats client","when":"command -v omawhats","action":"omawhats open"},
"whatsapp-quit": {"icon":"","label":"Quit WhatsApp","description":"Stop the daemon and take the widget off the bar","when":"omawhats running","action":"omawhats quit"},
```

The second row only shows while something is on (`omawhats running` says so by
its exit status), and `omarchy menu summon whatsapp` opens the client from a
keybinding.

## Use

- **Click** the icon: panel (power, QR, latest chats). Keys: `O` open the
  client, `W` open it in a window, `N` new chat, `P` power, `R` refresh,
  `?` shortcuts, `L` log out.
- **Right-click**: the full-screen client.
- **Middle-click**: turn on / off.
- From a keybinding or script:
  `omarchy-shell shell summon megamvb.omawhats '{}'` — the payload may carry
  `{"chat": "<jid>"}` or `{"action": "new" | "help" | "logout"}`, and
  `{"window": true}` for the window (`false` for the popup).

### Popup or window

The client opens either as the full-screen **popup** (right-click, `O`,
notifications) or as an ordinary **window** (the *Window* button in the panel,
`W`, or `omarchy-shell megamvb.omawhats window`): tiled like any app, on a
workspace, in the window switcher, and it stays open while you work
elsewhere — Esc does not close it, Super+W does. Ctrl+Shift+W (or the button
at the top of the client) switches between the two without losing the open
chat or what you were typing. While the window is open, anything that opens
the client (a notification's "Open", the panel's chats) goes to the window and
brings it forward. The window's class is `org.quickshell` and its title
`OmaWhats`, for Hyprland window rules (to make it float, for instance).

First time: turn it on and scan the QR under *WhatsApp → Settings → Linked
devices → Link a device*. Recent history arrives within seconds ("syncing…").

### Client keyboard shortcuts

Every action has a key. **F1** (or the keyboard button at the top) lists them.

| Keys | Action |
|---|---|
| Ctrl+F | Search chats |
| Alt+↑ / Alt+↓ | Previous / next chat |
| Alt+1 … Alt+9 | Open pinned chat 1–9 |
| Ctrl+N | New chat with a phone number |
| Ctrl+P | Pin / unpin the open chat |
| Alt+Shift+↑ / ↓ | Move the pinned chat up / down |
| Ctrl+W | Close the open chat |
| Enter | Send |
| Ctrl+E | Emoji picker (type to search, arrows and Enter to pick) |
| Ctrl+O | Attach photos or files (the system file chooser) |
| Ctrl+V | Paste: a screenshot or copied picture, or files copied in the file manager, are attached; text is pasted as usual |
| Ctrl+D | Send the attached photos as files, uncompressed |
| Esc | Remove the attachments, or cancel the reply being written |
| Tab | Switch between search and message |
| PgUp / PgDn | Scroll (older messages load at the top) |
| Alt+Home / Alt+End | Oldest loaded / newest message |
| Ctrl+↑ | Select messages with the keyboard: ↑/↓ move, R replies, E reacts, Enter opens the photo, file or link (or plays audio), Space plays / pauses audio, C copies the text, Esc goes back to typing |
| 1 … 6, ←/→ Enter, + | In the reaction bar: 👍 ❤️ 😂 😮 😢 🙏, pick with the arrows, or any other emoji |
| Alt+P | Play / pause the current audio |
| Alt+S | Audio speed 1× / 1.5× / 2× |
| ← / →, + / −, 0 / 1, R, O, Esc | In the image viewer: previous / next, zoom, fit / actual size, fetch a fresh copy, open outside, close |
| Ctrl+Shift+P | Turn OmaWhats on / off |
| Ctrl+R | Refresh; new QR code while linking |
| Ctrl+Shift+L | Log out (unlink this computer) |
| Ctrl+Shift+W | Switch between the popup and a window |
| F1 / Ctrl+/ | Show the shortcuts |
| Esc | Close a dialog, clear the search, or close the popup |

With the mouse: right-click a chat in the list to pin or unpin it; point at a
message for its react and reply buttons; click a reaction to add the same one
(or take yours back); click a quote to scroll to the message it answers.

## What it does

Text (send and receive), chats and groups, unread counts, read receipts,
delivered/read ticks, edited and deleted messages, notifications with an
"Open" button, and in the client:

- **photos** and **stickers** (animated ones too) inline; a click opens them
  large **inside the client**: wheel or +/− to zoom (about the cursor), drag
  to pan, double-click or 0/1 for fit / actual size, ←/→ through the chat's
  pictures, caption and sender shown, O (or the button) opens the system
  image viewer, and the ↻ button (R), always there, fetches the picture again
  whatever state it is in — a copy that opens can still be the wrong one. A
  picture not downloaded yet shows its thumbnail at full size while the real
  one is fetched; a download that fails says so under the picture, with a
  **Try again** button beside it, and so does a picture that will not show
  although a copy is here. Fetching again writes the new copy under a name of
  its own, so nothing old is shown in its place, and the copy that would not
  show is dropped only once the new one has landed;
- animated **GIFs** (WhatsApp sends MP4; the daemon converts it to animated
  WebP with ffmpeg);
- **videos** as a thumbnail with ▶ and length — a click downloads and opens it
  in the system player (mpv);
- **audio played right in the window**: ▶/⏸, a progress bar you can click to
  jump, elapsed/total time and speed 1× / 1.5× / 2×. It downloads on the first
  play; one audio plays at a time; consecutive voice messages play on, as in
  WhatsApp; it pauses when the client closes or the chat changes (Qt
  Multimedia with the ffmpeg backend — audio only, no GPU involved);
- **documents** as a row with name and size — a click downloads and opens;
- **clickable links**, plus the preview card the sender attached. Links open
  in the default browser through `omarchy-launch-browser`, which focuses the
  browser window; the client steps aside so the browser is not hidden behind it
  (files opened from a message do the same);
- emoji-only messages (up to 3) shown large;
- **replies**: a reply shows the message it answers (who wrote it, its text,
  a thumbnail for photos); a click scrolls to it, looking back through the
  stored messages if it is not loaded yet. To answer a message, point at it and
  click the reply arrow (or select it with Ctrl+↑ and press R); a bar above the
  message box shows what you are answering (Esc cancels);
- **reactions**: shown under each message, one chip per emoji with how many
  gave it (hover for who); yours is outlined. React with the smiley next to a
  message (or E on a selected one): the six usual ones, or + for any emoji.
  Picking your current reaction again takes it back. A reaction to one of your
  messages raises a notification;
- **emoji picker** (the smiley in the message box, or Ctrl+E): Omarchy's own
  emoji list, searchable by name, with the ones you used recently first;
- **sending photos and files**: the paperclip next to the message box (or
  Ctrl+O) opens the system file chooser, several at a time; you can also drop
  files on the window from the file manager, or paste with Ctrl+V (a
  screenshot, a copied picture, files copied in the file manager). They wait
  above the message box — remove any with its ×, or all with Esc — and Enter
  sends them, one message each, in order; what you typed goes as the first
  one's caption (and a reply being written answers with it). JPEG and PNG
  pictures go as **photos** (above 16 MB or ~50 megapixels they go as files
  instead, since a photo needs a thumbnail made of it); everything else — PDFs,
  videos, archives, documents — goes as a **file**, arriving exactly as it is,
  under its own name, up to WhatsApp's 2 GB. "Photos as files" (Ctrl+D) sends the photos
  uncompressed too. Sent files open straight from where they are on this
  computer;
- **older messages on demand**: scrolling up loads earlier messages in batches
  of 80 from the local copy, and past that asks your phone for more (the
  phone has to be online). The top of the conversation says what is happening
  ("Fetching older messages from your phone…", "No older messages", …);
- **pinned chats** — local to this computer, in the order you choose, kept in
  `~/.local/share/omawhats/pins.json`;
- **new chat** with any number: Ctrl+N (or the button next to the search),
  type the number with country code; it is checked with WhatsApp first, which
  also finds the right address for numbers without the ninth digit;
- **log out**: the button at the top of the client (or Ctrl+Shift+L, or `L`
  in the panel) unlinks this computer from the account, optionally deleting
  everything stored locally. It works with the daemon off too.

Photos, stickers and GIFs download on their own when a chat is opened with
the daemon on (turn off with "Download photos, stickers and GIFs
automatically"). Old media that expired on the server is requested from the
phone again, which has to be online.

Not (yet): videos sent as playable videos (they go as files), voice
messages, stickers, mentions, status, calls. Messages
stored before version 0.2 have no media pointer and still show only as
"📷 Photo".

## Files

| What | Where |
|---|---|
| Session + history | `~/.local/share/omawhats/whatsapp.db` (0600) |
| Pinned chats | `~/.local/share/omawhats/pins.json` |
| Recently used emojis | `~/.local/share/omawhats/recent-emoji.json` |
| Downloaded media and thumbnails, pasted pictures | `~/.cache/omawhats/media/` (safe to delete) |

The cache is tidied when the daemon starts and once a day after that: downloaded
files older than 30 days go, and so do the oldest ones whenever the downloads
pass 1 GB — a message whose file went shows its thumbnail again and fetches the
file if you open it. Thumbnails themselves are kept (they are tiny and cannot be
made again), and so are files still being transferred.

| Log | `~/.local/state/omawhats/daemon.log` |
| Socket | `$XDG_RUNTIME_DIR/omawhats.sock` |

To unlink the account: use **Log out** in the client, run
`omawhatsd logout` (add `--wipe` to also delete the local chats,
messages, reactions, media, pins and recent emojis), or remove the "OmaWhats" device on the phone (the
daemon notices and shows "This computer was unlinked from the phone").

## Warning

whatsmeow is an unofficial client. WhatsApp may suspend accounts that use
unofficial clients. For light personal use the risk is low, but it exists.

## Troubleshooting

- Links and files opened from the popup close it first (it would cover
  them); the window stays. The file chooser hides the popup while it is open
  and brings it back after.
- After updating, run `install-daemon` again and turn the daemon off and on once
  (middle-click the icon twice): a daemon started before the update is still the
  old binary, and an older one does not know how to send files.
- "Daemon not found at …" in the panel means the binary was never built: run
  `install-daemon` in the plugin folder.
- `omarchy-shell megamvb.omawhats debug` shows the panel's latest connection
  events; `omarchy-shell megamvb.omawhats-client debug` shows counts for the
  open client (never message content).
- After updating the plugin files the shell's hot-reload can keep old copies
  of them ("Function not found", a client that does not open, text that does
  not change). `omarchy restart shell` fixes it.
- Chats with numbers that are not in your address book show the number until
  a message arrives with the person's profile name.
- Older messages from the phone: the phone must be online and answer within
  45 s; if it does not, click the note at the top of the conversation to try
  again.

## Development

The repository root *is* the plugin folder — that is what `omarchy plugin add`
clones into place — so the QML files and `manifest.json` sit at the top, with
the daemon's Go source in `daemon/` and the `Model.js` tests in `tests/`
(neither is loaded by the shell).

```bash
make test      # go vet, the daemon tests and the Model.js tests
make validate  # the same checks `omarchy plugin add` runs on this folder
make install   # binary in ~/.local/bin, plugin files copied into the plugin folder
```

`make install` is for working from a checkout elsewhere (this one). If you
instead edit the clone the shell already loads, skip it — but keep the tree
fast-forwardable, or `omarchy plugin update` will refuse to pull.

Note that the two commands under [Install](#install) are for the other case,
a copy installed from the repository's URL. A folder that `make install`
filled is not a git checkout, so `omarchy plugin update` refuses it ("not a
git checkout"), and it holds only the files the shell loads — no
`install-daemon`, no `omawhats`, no `daemon/` to build from. On a development
machine `make install` is the whole update: it builds the daemon, installs
both commands and copies the plugin files.

Socket protocol (one JSON object per line):

- client → daemon: `hello`, `chats`,
  `history {chat, limit, before, beforeId, noAsk}`, `focus {chat}`,
  `send {chat, text, quote, req}` (quote: the id of the message answered),
  `sendfile {chat, path, text, quote, asDocument, req}` (an absolute path;
  text is the caption; files are sent one at a time, in order),
  `react {chat, id, emoji, req}` (empty emoji takes it back),
  `media {chat, id, open, req}`,
  `check {text, req}`, `pair`, `logout {wipe, req}`, `quit`, `ping`
- daemon → client: `state`, `chats`, `history {…, more, asked, end, offline}`,
  `older {chat, count, end, timeout}`, `message` (with `quote` and
  `reactions` when it has them, and `req` when it is the copy of one this
  client just sent), `status`, `sent`, `reacted {req, id, ok, error}`, `media`,
  `check {req, ok, jid, name, error}`, `loggedout {ok, error}`, `error`, `bye`
