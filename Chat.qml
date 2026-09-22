import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtMultimedia
import Quickshell
import Quickshell.Io
import Quickshell.Wayland
import qs.Commons
import qs.Ui
import "Model.js" as Model

// The client proper: conversations on the left, one of them on the right, a
// line to type in at the bottom.
//
// Summoned with an optional payload — by the bar panel, by the "Open" action
// on a notification, or by hand:
//   omarchy-shell shell summon megamvb.omawhats '{}'
//   {"chat": "<jid>"}    open that conversation
//   {"action": "new"}    the new-chat dialog ("logout" and "help" work too)
//
// With the daemon switched off it still opens, read-only, on what was stored.
// Every action has a key; F1 lists them (Model.SHORTCUTS).
Item {
  id: root

  property var shell: null
  property var manifest: null

  property bool opened: false
  // Shown as an ordinary window instead of the full-screen popup; see
  // open(), payload "window".
  property bool asWindow: false
  property string currentChat: ""
  property string query: ""
  property int listIndex: 0
  property double nowMs: Date.now()
  property var widgetSettings: ({})

  // Paging back through a conversation; see Model.historyHeader.
  //   "local"  a page of the stored copy is on its way
  //   "phone"  the daemon asked the phone for older messages
  //   "idle"   free to load more when the reader nears the top
  //   "end" | "timeout" | "offline-end"  nothing more for now
  property string historyPhase: "idle"
  property bool initialLoad: false
  // The phone said this was its last batch; the page reading it ends paging.
  property bool _phoneEnd: false

  // "" | "new" | "logout" | "help" | "image" | "react" | "emoji"
  property string dialog: ""
  // Keyboard selection in the conversation; -1 when typing.
  property int msgCursor: -1
  // Names learned while starting a chat that is not in the list yet.
  property var knownNames: ({})

  // Audio played in the window: one at a time, by message id.
  property string audioId: ""
  property real audioRate: 1.0
  // Audio waiting for its download to finish before it plays.
  property var _playAfter: ({})

  // The message the next one sent answers (Model.replyTarget), or null.
  property var replyTo: null
  // Files waiting to be sent with the next Enter: {path, name, size, photo}.
  property var attachments: []
  property bool attachAsFiles: false
  // The file chooser is open. It is an ordinary window, which would open
  // underneath the full-screen popup, so the popup steps aside meanwhile.
  property bool picking: false
  property var _statQueue: []
  // A quoted message being looked for further back (see jumpToMessage), and
  // the message briefly outlined once found.
  property string _jumpTarget: ""
  property int _jumpTries: 0
  property string flashId: ""

  // Reaction bar: the message it is for, this account's current reaction to
  // it, and the keyboard cursor over the choices (the last one is "+").
  property string reactFor: ""
  property string reactMine: ""
  property int reactCursor: 0
  property var _reactAnchor: null
  // Reactions shown before the daemon confirmed them, by request id, to put
  // back if it fails.
  property var _reactUndo: ({})

  // Emoji picker: for the message box or for a reaction.
  property string pickerFor: "composer"
  property string pickerQuery: ""
  property int pickerIndex: 0
  readonly property int pickerColumns: 8
  property var emojiAll: Model.fallbackEmojis()
  property var recentEmoji: []
  // The recent list as it was when the picker opened, so the grid does not
  // shift under the pointer while emojis are being picked.
  property var pickerRecent: []
  readonly property var pickerItems: Model.pickerList(emojiAll, pickerRecent, pickerQuery, pickerColumns)
  onPickerQueryChanged: pickerIndex = 0

  readonly property color background: Color.menu.background
  // The theme's menu background is often translucent; things floating over
  // the conversation need it opaque.
  readonly property color solidBackground: Qt.rgba(background.r, background.g, background.b, 1)
  readonly property color foreground: Color.menu.text
  readonly property color borderColor: Color.menu.border
  readonly property color scrim: Color.menu.scrim
  readonly property color accent: Color.accent
  readonly property var borderSpec: Border.surfaceSpec("menu", "border", borderColor, Math.max(1, Style.space(2)))
  readonly property color dim: Qt.darker(foreground, 1.5)
  readonly property string fontFamily: Style.font.menuFamily

  readonly property var filtered: Model.filterChats(wa.chats, query)
  readonly property var current: Model.findChat(wa.chats, currentChat)
  readonly property bool currentPinned: Model.isPinned(wa.pins, currentChat)
  // Typing a number that matches no conversation offers to start one.
  readonly property string newChatJid: Model.jidFromPhone(query) !== "" && !Model.findChat(wa.chats, Model.jidFromPhone(query))
    ? Model.jidFromPhone(query) : ""
  readonly property bool pairingView: Model.needsPairing(wa.daemonState, wa.live)

  readonly property string currentTitle: current ? Model.chatName(current)
    : (knownNames[currentChat] || Model.chatName({ jid: currentChat }))
  readonly property string currentSubtitle: {
    if (!currentChat) return ""
    if (current && current.group) return "Group"
    return Model.formatPhone(currentChat.split("@")[0])
  }

  function open(payloadJson) {
    var payload = {}
    try { payload = JSON.parse(String(payloadJson || "{}")) || {} } catch (e) { payload = {} }
    // "window": true or false picks the mode; without it an open client keeps
    // its mode (a notification's "Open" goes to the window) and a closed one
    // comes up as the popup.
    var wantWindow = typeof payload.window === "boolean" ? payload.window : (opened && asWindow)
    asWindow = wantWindow
    opened = true
    syncAppWindow()
    nowMs = Date.now()
    dialog = ""
    wa.refresh()
    var chat = String(payload.chat || "")
    if (chat !== "") selectChat(chat)
    else if (currentChat !== "") selectChat(currentChat)
    else Qt.callLater(function() { search.forceActiveFocus() })
    var action = String(payload.action || "")
    if (action === "new" || action === "logout" || action === "help") Qt.callLater(function() { root.openDialog(action) })
  }

  function close() {
    audioPlayer.pause()
    opened = false
    syncAppWindow()
    dialog = ""
    msgCursor = -1
    wa.focusOn("")
  }

  function dismiss() {
    if (shell && typeof shell.hide === "function" && manifest && manifest.id) shell.hide(manifest.id)
    else close()
  }

  // Esc with nothing left to cancel: the popup closes, a window stays
  // (Super+W closes it, like any other).
  function escapeOut() {
    if (!asWindow) dismiss()
  }

  // A new window does not always take the focus (and one already open may be
  // on another workspace), so each time it is shown it is also raised.
  function syncAppWindow() {
    appWindow.visible = opened && asWindow
    if (appWindow.visible) raiseTimer.restart()
  }

  // Popup ⇄ window, keeping the open chat and what was typed.
  function switchMode() {
    asWindow = !asWindow
    syncAppWindow()
    Qt.callLater(function() {
      if (root.currentChat !== "") composer.forceActiveFocus()
      else search.forceActiveFocus()
    })
  }

  // Bring the window forward, switching to its workspace — the way
  // omarchy-launch-or-focus does. The shell's own
  // windows are not in Quickshell's toplevel list, so it asks Hyprland.
  function raiseWindow() {
    Quickshell.execDetached(["sh", "-c",
      'a=$(hyprctl clients -j | jq -r --arg t "$1" \'first(.[] | select(.class == "org.quickshell" and .title == $t) | .address) // empty\')\n' +
      '[ -n "$a" ] || exit 0\n' +
      'hyprctl dispatch "hl.dsp.focus({ window = \\"address:$a\\" })" >/dev/null 2>&1 || hyprctl dispatch focuswindow "address:$a"',
      "sh", appWindow.title])
  }

  function selectChat(jid) {
    if (!jid) return
    if (jid !== currentChat) { stopAudio(); replyTo = null; clearAttachments() }
    _jumpTarget = ""
    currentChat = jid
    messages.clear()
    _mediaAsked = ({})
    msgCursor = -1
    historyPhase = "local"
    _phoneEnd = false
    initialLoad = true
    sendError.text = ""
    wa.focusOn(jid)
    wa.requestHistory(jid, 0, "")
    Qt.callLater(function() { composer.forceActiveFocus() })
  }

  function closeChat() {
    if (currentChat === "") return
    stopAudio()
    replyTo = null
    clearAttachments()
    _jumpTarget = ""
    currentChat = ""
    messages.clear()
    msgCursor = -1
    wa.focusOn("")
    Qt.callLater(function() { search.forceActiveFocus() })
  }

  function activateListIndex() {
    if (listIndex < filtered.length) selectChat(filtered[listIndex].jid)
    else if (newChatJid !== "") { openDialog("new", query); return }
    query = ""
    search.text = ""
  }

  function moveList(dy) {
    var n = filtered.length + (newChatJid !== "" ? 1 : 0)
    if (n === 0) return
    listIndex = Math.max(0, Math.min(n - 1, listIndex + dy))
    chatList.positionViewAtIndex(Math.min(listIndex, filtered.length - 1), ListView.Contain)
  }

  // Alt+Up/Down: the previous or next chat in the list as it is shown.
  function stepChat(delta) {
    var list = filtered
    if (list.length === 0) return
    var i = -1
    for (var k = 0; k < list.length; k++) if (list[k].jid === currentChat) { i = k; break }
    var next = i === -1 ? (delta > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, i + delta))
    if (list[next].jid !== currentChat) selectChat(list[next].jid)
    listIndex = next
    chatList.positionViewAtIndex(next, ListView.Contain)
  }

  function openPinned(n) {
    var shown = []
    for (var i = 0; i < wa.chats.length; i++) if (wa.chats[i].pinned) shown.push(wa.chats[i].jid)
    if (n < shown.length) selectChat(shown[n])
  }

  function togglePinCurrent() {
    if (currentChat !== "") wa.togglePin(currentChat)
  }

  // Ctrl+U: flag a chat to come back to. With the keyboard cursor in the chat
  // list it is that chat, which is never opened — opening one sends the read
  // receipt the mark is often there to avoid.
  function markUnreadTarget() {
    if (search.activeFocus && listIndex < filtered.length) {
      var c = filtered[listIndex]
      wa.markUnread(c.jid, !Model.isManualUnread(c))
      return
    }
    markUnreadCurrent()
  }

  // The open chat is a read one, so marking it unread closes it.
  function markUnreadCurrent() {
    if (currentChat === "") return
    var jid = currentChat
    closeChat()
    wa.markUnread(jid, true)
  }

  function moveCurrentPin(delta) {
    if (currentPinned) wa.movePin(currentChat, delta)
  }

  function toRow(m) {
    return {
      mid: String(m.id || ""),
      sender: String(m.sender || ""),
      senderName: String(m.senderName || ""),
      fromMe: m.fromMe === true,
      ts: Number(m.ts) || 0,
      body: String(m.text || ""),
      kind: String(m.kind || "text"),
      status: String(m.status || ""),
      edited: m.edited === true,
      // JSON strings: a ListModel turns nested objects into sub-models.
      mediaJson: m.media ? JSON.stringify(m.media) : "",
      linkJson: m.link ? JSON.stringify(m.link) : "",
      quoteJson: m.quote ? JSON.stringify(m.quote) : "",
      reactionsJson: m.reactions && m.reactions.length ? JSON.stringify(m.reactions) : "",
      mediaState: "",
      req: "",
      error: ""
    }
  }

  // ---- older messages

  // Loads the page before the oldest message held, once the reader is within
  // half a screen of the top (or the conversation does not fill the view).
  function maybeLoadOlder() {
    if (historyPhase !== "idle" || messages.count === 0 || currentChat === "" || dialog !== "") return
    var nearTop = messageList.contentY - messageList.originY < messageList.height * 0.5
    var fits = messageList.contentHeight <= messageList.height
    if (!nearTop && !fits) return
    // In a conversation barely taller than the view, "near the top" and "at the
    // newest message" are the same place: dropping the follow there left a
    // just-opened chat a little short of its last message (and pictures still
    // loading make the content look shorter than it turns out to be). The page
    // arrives above the reader either way, so only a reader who has scrolled
    // away from the end stops following it.
    loadOlder(false, fits || messageList.atYEnd)
  }

  function loadOlder(noAsk, keepFollow) {
    if (messages.count === 0) return
    var oldest = messages.get(0)
    historyPhase = "local"
    if (keepFollow !== true) messageList.follow = false
    wa.requestHistory(currentChat, oldest.ts, oldest.mid, noAsk === true)
  }

  // Header click after the phone did not answer.
  function retryOlder() {
    if (historyPhase !== "timeout") return
    historyPhase = "idle"
    loadOlder(false)
  }

  // The message the reader is looking at, and where it sits on screen, so
  // rows inserted above it do not move it.
  function captureAnchor() {
    var idx = messageList.indexAt(Style.space(20), messageList.contentY + Style.space(2))
    if (idx < 0) idx = 0
    var item = messageList.itemAtIndex(idx)
    return { index: idx, offset: item ? item.y - messageList.contentY : 0 }
  }

  function restoreAnchor(anchor, inserted) {
    messageList.positionViewAtIndex(anchor.index + inserted, ListView.Beginning)
    var item = messageList.itemAtIndex(anchor.index + inserted)
    if (item) messageList.contentY = item.y - anchor.offset
  }

  function historyPhaseAfter(info, count, before) {
    if (!before) return info.offline && !info.more && count > 0 ? "offline-end" : "idle"
    if (info.asked) return "phone"
    if (info.end) return "end"
    if (!info.more && _phoneEnd) return "end"
    if (info.offline && !info.more) return "offline-end"
    return "idle"
  }

  // ---- media and links

  // Ids already asked for, so a delegate scrolled in and out of view does not
  // ask again.
  property var _mediaAsked: ({})

  function autoFetch(id, media) {
    if (!media || !Model.isAutoDownload(media) || !wa.live || root.autoDownload !== true) return
    if (media.type === "gif" ? media.anim : media.file) return
    if (_mediaAsked[id]) return
    _mediaAsked[id] = true
    var idx = indexOfId(id)
    if (idx >= 0) messages.setProperty(idx, "mediaState", "loading")
    wa.requestMedia(currentChat, id, false)
  }

  // Click on an attachment: open what is on disk, or fetch it first.
  function openMedia(id, media) {
    if (!media) return
    if (Model.isAudio(media)) { toggleAudio(id, media); return }
    if (Model.isViewable(media)) { openViewer(id); return }
    if (media.file) { openFile(media.file); return }
    var idx = indexOfId(id)
    if (!wa.live) {
      if (idx >= 0) messages.setProperty(idx, "mediaState", "Turn OmaWhats on to download")
      return
    }
    if (idx >= 0) messages.setProperty(idx, "mediaState", "loading")
    wa.requestMedia(currentChat, id, true)
  }

  // Ask for an attachment again after a download failed, or was never made:
  // the note saying it was already asked for has to go, or the next scroll
  // past it would take this for a repeat and drop it. When a file is already
  // here and still nothing can be shown, the ask goes with force, so the
  // daemon drops that copy instead of handing the same one back.
  function retryMedia(id) {
    if (!id) return
    var idx = indexOfId(id)
    if (idx < 0) return
    if (!wa.live) {
      messages.setProperty(idx, "mediaState", "Turn OmaWhats on to download")
      return
    }
    var media = Model.parseJson(messages.get(idx).mediaJson)
    delete _mediaAsked[id]
    messages.setProperty(idx, "mediaState", "loading")
    wa.requestMedia(currentChat, id, false, Model.hasCachedFile(media))
  }

  // Anything opened outside the shell would land underneath the full-screen
  // popup, so the popup steps aside first (a window stays). Links go through
  // Omarchy's browser launcher, which also focuses the browser window.
  function openLink(url) {
    url = String(url || "")
    if (url === "") return
    if (!/^[a-z][a-z0-9+.-]*:/i.test(url)) url = "http://" + url
    if (!asWindow) dismiss()
    Quickshell.execDetached(["sh", "-c",
      'if command -v omarchy-launch-browser >/dev/null 2>&1; then exec omarchy-launch-browser "$1"; else exec xdg-open "$1"; fi',
      "sh", url])
  }

  function openFile(path) {
    if (!asWindow) dismiss()
    Qt.openUrlExternally(Model.fileUrl(path))
  }

  // ---- audio

  MediaPlayer {
    id: audioPlayer
    audioOutput: AudioOutput { }
    playbackRate: root.audioRate
    onErrorOccurred: function(error, errorString) {
      root.showToast("Could not play this audio" + (errorString ? ": " + errorString : ""))
    }
    onMediaStatusChanged: if (mediaStatus === MediaPlayer.EndOfMedia) root.audioEnded()
  }

  // Play, pause or resume a message's audio, downloading it first if needed.
  function toggleAudio(id, media) {
    if (!Model.isAudio(media)) return
    if (audioId === id && audioPlayer.source.toString() !== "") {
      if (audioPlayer.playbackState === MediaPlayer.PlayingState) audioPlayer.pause()
      else audioPlayer.play()
      return
    }
    var idx = indexOfId(id)
    if (!media.file) {
      if (!wa.live) {
        if (idx >= 0) messages.setProperty(idx, "mediaState", "Turn OmaWhats on to download")
        return
      }
      _playAfter[id] = true
      if (idx >= 0) messages.setProperty(idx, "mediaState", "loading")
      wa.requestMedia(currentChat, id, false)
      return
    }
    audioPlayer.stop()
    audioId = id
    audioPlayer.source = Model.fileUrl(media.file)
    audioPlayer.play()
  }

  function toggleCurrentAudio() {
    if (audioId === "") { showToast("No audio playing"); return }
    if (audioPlayer.playbackState === MediaPlayer.PlayingState) audioPlayer.pause()
    else audioPlayer.play()
  }

  function stopAudio() {
    audioPlayer.stop()
    audioPlayer.source = ""
    audioId = ""
    _playAfter = ({})
  }

  function seekAudio(id, media, fraction) {
    if (audioId !== id) { toggleAudio(id, media); return }
    if (audioPlayer.duration > 0) audioPlayer.position = Math.max(0, Math.min(1, fraction)) * audioPlayer.duration
    if (audioPlayer.playbackState !== MediaPlayer.PlayingState) audioPlayer.play()
  }

  function cycleAudioRate() {
    audioRate = Model.nextRate(audioRate)
    showToast("Audio speed " + Model.rateLabel(audioRate))
  }

  // Like WhatsApp: a voice message that ends plays the next one, if the next
  // message is a voice message too.
  function audioEnded() {
    var id = audioId
    audioPlayer.stop()
    var i = indexOfId(id)
    if (i < 0 || i + 1 >= messages.count) return
    var cur = Model.parseJson(messages.get(i).mediaJson)
    var next = messages.get(i + 1)
    var nm = Model.parseJson(next.mediaJson)
    if (cur && cur.voice && nm && nm.type === "audio" && nm.voice) toggleAudio(next.mid, nm)
  }

  // ---- image viewer (photos, stickers and GIFs, inside the client)

  property string viewerId: ""
  property string viewerMediaJson: ""
  property var viewerIds: []
  readonly property var viewerMedia: Model.parseJson(viewerMediaJson)
  readonly property var viewerSrc: Model.viewerSource(viewerMedia)
  readonly property int viewerPos: viewerIds.indexOf(viewerId)

  function openViewer(id) {
    var ids = []
    for (var i = 0; i < messages.count; i++)
      if (Model.isViewable(Model.parseJson(messages.get(i).mediaJson))) ids.push(messages.get(i).mid)
    if (ids.indexOf(id) === -1) return
    viewerIds = ids
    msgCursor = -1
    dialog = "image"
    showInViewer(id)
    Qt.callLater(function() { viewerKeys.forceActiveFocus() })
  }

  function showInViewer(id) {
    var idx = indexOfId(id)
    if (idx < 0) return
    viewerId = id
    viewerMediaJson = messages.get(idx).mediaJson
    viewer.zoom = 1
    // Only the thumbnail is here yet: fetch the real picture; the viewer
    // swaps it in when onMediaResult arrives.
    if (!Model.viewerSource(viewerMedia).full && wa.live && messages.get(idx).mediaState !== "loading") {
      messages.setProperty(idx, "mediaState", "loading")
      wa.requestMedia(currentChat, id, false)
    }
  }

  function viewerStep(delta) {
    var i = viewerPos + delta
    if (i >= 0 && i < viewerIds.length) showInViewer(viewerIds[i])
  }

  function viewerRow() {
    var idx = indexOfId(viewerId)
    return idx >= 0 ? messages.get(idx) : null
  }

  readonly property bool autoDownload: widgetSettings.autoDownloadMedia !== false
  readonly property string linkColor: String(root.accent)

  // See Model.makeRowIndex: row numbers by id, forgotten whenever rows move.
  readonly property var _rowIndex: Model.makeRowIndex()
  function forgetRowIndex() { _rowIndex.forget() }

  function indexOfId(id) {
    return _rowIndex.find(id,
      function() { return messages.count },
      function(i) { return messages.get(i).mid })
  }

  function indexOfPending(field, value) {
    for (var i = messages.count - 1; i >= 0; i--) {
      var r = messages.get(i)
      if (r.status === "pending" && r[field] === value) return i
    }
    return -1
  }

  function followEnd() {
    if (messageList.follow) messageList.positionViewAtEnd()
  }

  function scrollToEnd() {
    messageList.follow = true
    Qt.callLater(function() { messageList.positionViewAtEnd() })
  }

  function sendComposer() {
    if (attachments.length > 0 && currentChat) { sendAttachments(); return }
    var text = composer.text
    if (text.trim() === "" || !currentChat) return
    if (!wa.canSend) {
      sendError.text = "Turn OmaWhats on and wait for it to connect to send."
      return
    }
    var quote = replyTo
    var req = wa.send(currentChat, text, quote ? quote.id : "")
    if (req === "") {
      sendError.text = "Could not reach the daemon."
      return
    }
    sendError.text = ""
    // Shown straight away with a clock; the daemon's echo replaces it.
    var row = toRow({ id: "pending-" + req, text: text, fromMe: true, ts: Math.floor(Date.now() / 1000), status: "pending", senderName: "You", quote: quote })
    row.req = req
    messages.append(row)
    composer.text = ""
    replyTo = null
    scrollToEnd()
  }

  // ---- attachments

  function pickFiles() {
    if (currentChat === "" || picking || pickProc.running) return
    msgCursor = -1
    if (dialog !== "") dialog = ""
    picking = true
    pickProc.command = ["omarchy-file-select", "--title", "Send to " + currentTitle, "--multiple"]
    pickProc.running = true
  }

  function pickDone(exitCode, text) {
    if (!picking) return
    picking = false
    if (exitCode === 2) showToast("The file chooser did not open")
    var paths = String(text || "").split("\n").filter(function(p) { return p !== "" })
    if (paths.length > 0) addFiles(paths)
    Qt.callLater(function() { if (root.currentChat !== "") composer.forceActiveFocus() })
  }

  // Paths from the chooser, a drop or the clipboard: checked (size, folders)
  // and added to the tray above the message box.
  function addFiles(paths) {
    if (!paths || paths.length === 0) return
    if (currentChat === "") { showToast("Open a chat first"); return }
    _statQueue = _statQueue.concat(paths)
    if (!statProc.running) runStat()
  }

  function runStat() {
    if (_statQueue.length === 0) return
    statProc.command = ["stat", "-L", "--printf", "%s\t%F\t%n\n", "--"].concat(_statQueue)
    _statQueue = []
    statProc.running = true
  }

  function statDone(text) {
    var r = Model.parseStat(text)
    var added = Model.addAttachments(attachments, r.files)
    if (currentChat !== "") attachments = added.list
    var problems = added.problems.slice()
    if (r.skipped.length === 1) problems.push(Model.baseName(r.skipped[0]) + " is a folder, not a file")
    else if (r.skipped.length > 1) problems.push(r.skipped.length + " folders left out")
    if (problems.length > 0) showToast(problems.join(" · "))
    if (_statQueue.length > 0) runStat()
    else if (currentChat !== "" && dialog === "") composer.forceActiveFocus()
  }

  function removeAttachment(i) {
    var list = attachments.slice()
    list.splice(i, 1)
    attachments = list
    if (list.length === 0) attachAsFiles = false
    composer.forceActiveFocus()
  }

  function clearAttachments() {
    attachments = []
    attachAsFiles = false
  }

  function toggleAttachAsFiles() {
    if (!Model.attachmentsSummary(attachments, false).match(/photo/)) { showToast("No photos attached"); return }
    attachAsFiles = !attachAsFiles
    showToast(attachAsFiles ? "Photos go as files, uncompressed" : "Photos go as photos")
  }

  // One message per file, in order; what was typed is the first one's
  // caption, and a reply being written answers with the first one.
  function sendAttachments() {
    if (!wa.canSend) {
      sendError.text = "Turn OmaWhats on and wait for it to connect to send."
      return
    }
    var caption = composer.text.trim()
    var quote = replyTo
    var now = Math.floor(Date.now() / 1000)
    var list = attachments
    for (var i = 0; i < list.length; i++) {
      var f = list[i]
      var cap = i === 0 ? caption : ""
      var q = i === 0 ? quote : null
      var req = wa.sendFile(currentChat, f.path, cap, q ? q.id : "", attachAsFiles && f.photo)
      if (req === "") {
        sendError.text = "Could not reach the daemon."
        attachments = list.slice(i)
        return
      }
      var row = toRow({ id: "pending-" + req, text: cap, fromMe: true, ts: now, status: "pending", senderName: "You",
        quote: q, media: Model.pendingMedia(f, cap, attachAsFiles) })
      row.req = req
      row.mediaState = "sending"
      messages.append(row)
    }
    sendError.text = ""
    composer.text = ""
    replyTo = null
    clearAttachments()
    scrollToEnd()
  }

  // Ctrl+V: a screenshot or copied picture, or files copied in the file
  // manager, are attached; anything else is pasted as text.
  function pasteClipboard() {
    if (pasteTypes.running || pasteUris.running || pasteImage.running) return
    pasteTypes.running = true
  }

  function pasteTypesDone(types) {
    var kind = Model.pasteKind(types)
    if (kind === "files" && currentChat !== "") {
      pasteUris.running = true
    } else if (kind === "image" && currentChat !== "") {
      var type = Model.pasteImageType(types)
      pasteImage.dest = wa.cacheDir + "/" + Model.pastedName(new Date(), type)
      pasteImage.command = ["sh", "-c", 'umask 077 && mkdir -p "$1" && wl-paste --no-newline --type "$2" > "$3"', "sh", wa.cacheDir, type, pasteImage.dest]
      pasteImage.running = true
    } else {
      composer.paste()
    }
  }

  function pasteUrisDone(text) {
    var paths = Model.pathsFromUris(text)
    if (paths.length > 0) addFiles(paths)
    else composer.paste()
  }

  // ---- replies

  // Pending, failed and deleted messages cannot be answered or reacted to.
  function canAct(r) {
    return !!r && r.status !== "pending" && r.status !== "failed" && r.kind !== "revoked" && String(r.mid).indexOf("pending-") !== 0
  }

  function startReply(i) {
    if (i < 0 || i >= messages.count) return
    var r = messages.get(i)
    if (!canAct(r)) { showToast("Can't reply to this message"); return }
    var target = Model.replyTarget(r)
    if (target.name === "") target.name = current && current.group ? "" : currentTitle
    replyTo = target
    msgCursor = -1
    composer.forceActiveFocus()
  }

  function cancelReply() { replyTo = null }

  // Scrolls to a quoted message, paging back through the stored copy (never
  // the phone) for a few pages if it is not loaded yet.
  function jumpToMessage(id) {
    if (!id) return
    var idx = indexOfId(id)
    if (idx >= 0) { showMessage(idx); return }
    _jumpTarget = id
    _jumpTries = 6
    continueJump()
  }

  function showMessage(idx) {
    messageList.follow = false
    messageList.positionViewAtIndex(idx, ListView.Center)
    flashId = messages.get(idx).mid
    flashTimer.restart()
  }

  function continueJump() {
    if (_jumpTarget === "") return
    var idx = indexOfId(_jumpTarget)
    if (idx >= 0) { _jumpTarget = ""; showMessage(idx); return }
    if (historyPhase === "local") return   // a page is on its way
    if (_jumpTries > 0 && historyPhase === "idle") { _jumpTries -= 1; loadOlder(true); return }
    _jumpTarget = ""
    showToast("The original message is not stored here")
  }

  Timer { id: flashTimer; interval: 1400; onTriggered: root.flashId = "" }

  // ---- reactions

  function openReactions(i) {
    if (i < 0 || i >= messages.count) return
    var r = messages.get(i)
    if (!canAct(r)) { showToast("Can't react to this message"); return }
    if (!wa.canSend) { showToast("Turn OmaWhats on to react"); return }
    reactFor = r.mid
    reactMine = Model.myReaction(Model.parseJson(r.reactionsJson) || [])
    var mine = Model.QUICK_REACTIONS.indexOf(reactMine)
    reactCursor = mine >= 0 ? mine : 0
    var item = messageList.itemAtIndex(i)
    _reactAnchor = item ? { rect: item.bubbleRect(popupLayer), fromMe: r.fromMe } : null
    var p = placePopup(reactBar.implicitWidth, reactBar.implicitHeight, _reactAnchor)
    popupLayer.barX = p.x
    popupLayer.barY = p.y
    dialog = "react"
    Qt.callLater(function() { reactBar.forceActiveFocus() })
  }

  // Sets this account's reaction on a message, or takes it back when it is the
  // one already there. Shown at once; put back if the daemon says no.
  function react(id, emoji) {
    var idx = indexOfId(id)
    if (idx < 0 || !emoji) return
    var r = messages.get(idx)
    var list = Model.parseJson(r.reactionsJson) || []
    var send = Model.reactionToggle(Model.myReaction(list), emoji)
    var req = wa.react(currentChat, id, send)
    if (req === "") { showToast("Turn OmaWhats on to react"); return }
    _reactUndo[req] = { id: id, json: r.reactionsJson }
    messages.setProperty(idx, "reactionsJson", JSON.stringify(Model.withMyReaction(list, send)))
  }

  function pickReaction(emoji) {
    var id = reactFor
    closeDialog()
    react(id, emoji)
  }

  // Where a popup of size w×h goes: above the anchor (below it when there is
  // no room), aligned with the side the message sits on, kept on the card.
  function placePopup(w, h, anchor) {
    var m = Style.space(8)
    var x = (popupLayer.width - w) / 2
    var y = (popupLayer.height - h) / 2
    if (anchor && anchor.rect) {
      var a = anchor.rect
      x = anchor.fromMe ? a.x + a.width - w : a.x
      y = a.y - h - Style.space(6)
      if (y < m) y = a.y + a.height + Style.space(6)
    }
    return {
      x: Math.max(m, Math.min(popupLayer.width - w - m, x)),
      y: Math.max(m, Math.min(popupLayer.height - h - m, y))
    }
  }

  // ---- emoji picker

  // forWhat: "composer" inserts into the message box (the picker stays open
  // for more); "react" reacts to reactFor with the one picked.
  function openEmojiPicker(forWhat) {
    if (currentChat === "") return
    pickerFor = forWhat === "react" ? "react" : "composer"
    pickerQuery = ""
    pickerIndex = 0
    pickerRecent = recentEmoji
    var anchor = pickerFor === "react" ? _reactAnchor
      : { rect: composer.mapToItem(popupLayer, 0, 0, composer.width, composer.height), fromMe: false }
    var p = placePopup(pickerPanel.width, pickerPanel.height, anchor)
    popupLayer.pickX = p.x
    popupLayer.pickY = p.y
    msgCursor = pickerFor === "react" ? msgCursor : -1
    dialog = "emoji"
    Qt.callLater(function() { pickerGrid.positionViewAtBeginning(); pickerKeys.forceActiveFocus() })
  }

  function pickEmoji(e) {
    if (!e) return
    recentEmoji = Model.pushRecent(recentEmoji, e, 24)
    recentFile.setText(JSON.stringify(recentEmoji) + "\n")
    if (pickerFor === "react") { pickReaction(e); return }
    composer.insert(composer.cursorPosition, e)
  }

  // Arrow keys over the grid, stepping over the blank cells that pad the
  // recent row.
  function movePicker(delta) {
    var items = pickerItems
    if (items.length === 0) return
    var i = Math.max(0, Math.min(items.length - 1, pickerIndex + delta))
    var step = delta > 0 ? 1 : -1
    while (i > 0 && i < items.length - 1 && items[i].e === "") i += step
    if (items[i].e === "") return
    pickerIndex = i
    pickerGrid.positionViewAtIndex(i, GridView.Contain)
  }

  // ---- keyboard selection of messages

  function enterMessageCursor() {
    if (currentChat === "" || messages.count === 0) return
    msgCursor = messages.count - 1
    messageList.follow = false
    messageList.forceActiveFocus()
    messageList.positionViewAtIndex(msgCursor, ListView.Contain)
  }

  function leaveMessageCursor() {
    msgCursor = -1
    composer.forceActiveFocus()
  }

  function moveMessageCursor(delta) {
    if (msgCursor < 0) return
    msgCursor = Math.max(0, Math.min(messages.count - 1, msgCursor + delta))
    messageList.positionViewAtIndex(msgCursor, ListView.Contain)
    if (msgCursor < 3) maybeLoadOlder()
  }

  function activateMessage(i) {
    if (i < 0 || i >= messages.count) return
    var r = messages.get(i)
    var media = Model.parseJson(r.mediaJson)
    if (media) { openMedia(r.mid, media); return }
    var url = Model.firstLink(r.body, Model.parseJson(r.linkJson))
    if (url !== "") openLink(url)
    else showToast("Nothing to open in this message")
  }

  function copyMessage(i) {
    if (i < 0 || i >= messages.count) return
    var r = messages.get(i)
    var text = Model.bubbleText(r.body, Model.parseJson(r.mediaJson))
    if (text === "") { showToast("No text to copy"); return }
    Quickshell.execDetached(["wl-copy", "--", text])
    showToast("Copied")
  }

  property string toastText: ""
  function showToast(t) {
    toastText = t
    toastTimer.restart()
  }
  Timer { id: toastTimer; interval: 1600; onTriggered: root.toastText = "" }

  // ---- dialogs

  function openDialog(name, prefill) {
    if (name === "logout" && !wa.daemonState.paired && !wa.live) { showToast("Not linked to an account"); return }
    msgCursor = -1
    dialog = name
    if (name === "new") {
      newChatField.text = prefill ? String(prefill) : ""
      newChat.error = ""
      newChat.req = ""
      Qt.callLater(function() { newChatField.forceActiveFocus(); if (prefill) root.checkNewChat() })
    } else if (name === "logout") {
      logoutSheet.wipe = false
      logoutSheet.choice = 0
      logoutSheet.error = ""
      Qt.callLater(function() { logoutSheet.forceActiveFocus() })
    } else if (name === "help") {
      Qt.callLater(function() { helpSheet.forceActiveFocus() })
    }
  }

  function closeDialog() {
    dialog = ""
    Qt.callLater(function() {
      if (root.msgCursor >= 0) messageList.forceActiveFocus()
      else if (root.currentChat !== "") composer.forceActiveFocus()
      else search.forceActiveFocus()
    })
  }

  function checkNewChat() {
    if (!wa.online) { newChat.error = "Turn OmaWhats on and wait for it to connect first."; return }
    if (Model.jidFromPhone(newChatField.text) === "") {
      newChat.error = "Type the full number, with country and area code (e.g. +55 11 98765-4321)."
      return
    }
    newChat.error = ""
    newChat.req = wa.checkNumber(newChatField.text)
    if (newChat.req === "") newChat.error = "Could not reach the daemon."
  }

  // ---- keys
  //
  // One handler for every shortcut, called by whichever item has focus (both
  // text fields, the message list, the card) before it handles the key itself.
  function handleKey(event) {
    if (dialog !== "") return false
    var mods = event.modifiers
    var ctrl = (mods & Qt.ControlModifier) !== 0
    var alt = (mods & Qt.AltModifier) !== 0
    var shift = (mods & Qt.ShiftModifier) !== 0
    var k = event.key

    if (k === Qt.Key_F1 || (ctrl && (k === Qt.Key_Slash || k === Qt.Key_Question))) { openDialog("help"); return true }
    if (ctrl && !alt) {
      if (k === Qt.Key_F && !shift) { search.forceActiveFocus(); search.selectAll(); return true }
      if (k === Qt.Key_N && !shift) { openDialog("new"); return true }
      if (k === Qt.Key_P && !shift) { togglePinCurrent(); return true }
      if (k === Qt.Key_P && shift) { if (!wa.starting && !wa.stopping) wa.togglePower(); return true }
      if (k === Qt.Key_L && shift) { openDialog("logout"); return true }
      if (k === Qt.Key_R && !shift) {
        if (pairingView && wa.daemonState.state !== "pairing") wa.pair()
        else wa.refresh()
        return true
      }
      if (k === Qt.Key_U && !shift) { markUnreadTarget(); return true }
      if (k === Qt.Key_W && !shift) { closeChat(); return true }
      if (k === Qt.Key_W && shift) { switchMode(); return true }
      if (k === Qt.Key_E && !shift) { openEmojiPicker("composer"); return true }
      if (k === Qt.Key_O && !shift) { pickFiles(); return true }
      if (k === Qt.Key_D && !shift && attachments.length > 0) { toggleAttachAsFiles(); return true }
      if (k === Qt.Key_Up && !shift && msgCursor < 0) { enterMessageCursor(); return true }
    }
    if (alt && !ctrl) {
      if (shift && k === Qt.Key_Up) { moveCurrentPin(-1); return true }
      if (shift && k === Qt.Key_Down) { moveCurrentPin(1); return true }
      if (!shift && k === Qt.Key_Up) { stepChat(-1); return true }
      if (!shift && k === Qt.Key_Down) { stepChat(1); return true }
      if (k >= Qt.Key_1 && k <= Qt.Key_9) { openPinned(k - Qt.Key_1); return true }
      if (k === Qt.Key_Home) {
        messageList.follow = false
        messageList.positionViewAtBeginning()
        maybeLoadOlder()
        return true
      }
      if (k === Qt.Key_End) { scrollToEnd(); return true }
      if (k === Qt.Key_P && !shift) { toggleCurrentAudio(); return true }
      if (k === Qt.Key_S && !shift) { cycleAudioRate(); return true }
    }
    if (!ctrl && !alt && currentChat !== "") {
      if (k === Qt.Key_PageUp) {
        messageList.follow = false
        messageList.flick(0, Style.space(2500))
        maybeLoadOlder()
        return true
      }
      if (k === Qt.Key_PageDown) { messageList.flick(0, -Style.space(2500)); return true }
    }
    return false
  }

  ListModel { id: messages; onCountChanged: root.forgetRowIndex() }

  // Counts only — no message content leaves through here.
  IpcHandler {
    target: "megamvb.omawhats-client"
    function debug(): string {
      return JSON.stringify({ opened: root.opened, live: wa.live, chats: wa.chats.length, pins: wa.pins.length,
        messages: messages.count, phase: root.historyPhase, dialog: root.dialog }) + "\n" + wa.debugTrace
    }
    function newChat(): string { root.openDialog("new"); return "ok" }
    function shortcuts(): string { root.openDialog("help"); return "ok" }
  }

  Service {
    id: wa
    settings: root.widgetSettings

    onHistoryReceived: function(chat, msgs, before, info) {
      if (chat !== root.currentChat) return
      if (!before) {
        messages.clear()
        for (var i = 0; i < msgs.length; i++) messages.append(root.toRow(msgs[i]))
        root.initialLoad = false
        root.historyPhase = root.historyPhaseAfter(info, msgs.length, before)
        root.scrollToEnd()
        Qt.callLater(root.maybeLoadOlder)
        return
      }
      var phase = root.historyPhaseAfter(info, msgs.length, before)
      if (msgs.length === 0) {
        root.historyPhase = phase
        if (root._jumpTarget !== "") { root._jumpTarget = ""; root.showToast("The original message is not stored here") }
        return
      }
      var anchor = root.captureAnchor()
      for (var j = msgs.length - 1; j >= 0; j--) messages.insert(0, root.toRow(msgs[j]))
      if (root.msgCursor >= 0) root.msgCursor += msgs.length
      var n = msgs.length
      // The phase stays busy until the view is back on the same message, so
      // the scroll that restoring causes cannot trigger another load.
      Qt.callLater(function() {
        root.restoreAnchor(anchor, n)
        root.historyPhase = phase
        if (root._jumpTarget !== "") root.continueJump()
        else root.maybeLoadOlder()
      })
    }

    onOlderReceived: function(chat, count, end, timedOut) {
      if (chat !== root.currentChat || root.historyPhase !== "phone") return
      if (timedOut) { root.historyPhase = "timeout"; return }
      if (count > 0) {
        root._phoneEnd = end
        root.loadOlder(true)
      } else {
        root.historyPhase = "end"
      }
    }

    onMessageReceived: function(chat, m, update, req) {
      if (chat !== root.currentChat || !m) return
      var row = root.toRow(m)
      var idx = root.indexOfId(row.mid)
      if (idx >= 0) { messages.set(idx, row); root.forgetRowIndex(); return }
      if (update) return
      if (req !== "") {
        var byReq = root.indexOfPending("req", req)
        if (byReq >= 0) { messages.set(byReq, row); root.forgetRowIndex(); return }
      }
      if (row.fromMe) {
        var p = root.indexOfPending("body", row.body)
        if (p >= 0) { messages.set(p, row); root.forgetRowIndex(); return }
      }
      var atEnd = messageList.atYEnd
      messages.append(row)
      if (atEnd || row.fromMe) root.scrollToEnd()
    }

    onStatusReceived: function(chat, id, status) {
      if (chat !== root.currentChat) return
      var idx = root.indexOfId(id)
      if (idx >= 0) messages.setProperty(idx, "status", status)
    }

    onSendResult: function(req, chat, ok, error, id) {
      var idx = root.indexOfPending("req", req)
      if (idx < 0) return
      messages.setProperty(idx, "mediaState", "")
      if (ok) {
        if (root.indexOfId(id) >= 0) { messages.remove(idx); return }
        messages.setProperty(idx, "mid", id)
        messages.setProperty(idx, "status", "sent")
        root.forgetRowIndex()
      } else {
        messages.setProperty(idx, "status", "failed")
        messages.setProperty(idx, "error", error)
        sendError.text = "Could not send: " + error
      }
    }

    // A daemon started before an update does not know "sendfile".
    onDaemonError: function(error) {
      if (error.indexOf("sendfile") < 0) return
      for (var i = 0; i < messages.count; i++) {
        if (messages.get(i).mediaState !== "sending") continue
        messages.setProperty(i, "mediaState", "")
        messages.setProperty(i, "status", "failed")
      }
      sendError.text = "This daemon is older than the plugin: turn OmaWhats off and on again to send files."
    }

    onReactResult: function(req, chat, id, ok, error) {
      var undo = root._reactUndo[req]
      delete root._reactUndo[req]
      if (ok || !undo) return
      root.showToast("Could not react: " + (error || "unknown error"))
      if (chat !== root.currentChat) return
      var idx = root.indexOfId(undo.id)
      if (idx >= 0) messages.setProperty(idx, "reactionsJson", undo.json)
    }

    onMediaResult: function(req, chat, id, ok, error, media, open) {
      if (chat !== root.currentChat) return
      var idx = root.indexOfId(id)
      if (idx < 0) return
      var playAfter = root._playAfter[id] === true
      delete root._playAfter[id]
      if (!ok) {
        messages.setProperty(idx, "mediaState", error || "Download failed")
        return
      }
      messages.setProperty(idx, "mediaState", "")
      messages.setProperty(idx, "mediaJson", JSON.stringify(media))
      if (id === root.viewerId) root.viewerMediaJson = JSON.stringify(media)
      if (playAfter && root.opened) root.toggleAudio(id, media)
      if (open && media && media.file && root.opened) root.openFile(media.file)
    }

    onCheckResult: function(req, ok, jid, name, error) {
      if (req !== newChat.req) return
      newChat.req = ""
      if (!ok) { newChat.error = error || "Could not check the number."; return }
      if (name !== "") {
        var names = root.knownNames
        names[jid] = name
        root.knownNames = names
      }
      root.dialog = ""
      root.query = ""
      search.text = ""
      root.selectChat(jid)
    }

    onLogoutFinished: function(ok, error) {
      if (root.dialog !== "logout") return
      if (ok) {
        root.closeChat()
        root.closeDialog()
      } else {
        logoutSheet.error = error
      }
    }

    onBecameLive: if (root.opened && root.currentChat !== "") {
      root._mediaAsked = ({})
      root.historyPhase = "local"
      root._phoneEnd = false
      wa.requestHistory(root.currentChat, 0, "")
    }

    onLiveChanged: if (!live && root.historyPhase === "phone") root.historyPhase = "idle"

    // Back online (a reconnect, not a restart): paging may reach the phone again.
    onOnlineChanged: if (online && root.historyPhase === "offline-end") {
      root.historyPhase = "idle"
      root.maybeLoadOlder()
    }
  }

  // ---- file chooser, clipboard, file checks

  Process {
    id: pickProc
    running: false
    property string out: ""
    property int code: -1
    property bool streamDone: false
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: { pickProc.out = String(text || ""); pickProc.streamDone = true; pickProc.finish() }
    }
    onStarted: { out = ""; code = -1; streamDone = false }
    onExited: function(exitCode) { code = exitCode; finish() }
    // A chooser that never started exits with nothing: give the popup back.
    onRunningChanged: if (!running) pickFallback.restart()
    function finish() { if (streamDone && code >= 0) root.pickDone(code, out) }
  }
  Timer { id: pickFallback; interval: 500; onTriggered: if (root.picking) root.pickDone(2, "") }

  Process {
    id: statProc
    running: false
    stdout: StdioCollector { waitForEnd: true; onStreamFinished: root.statDone(text) }
  }

  Process {
    id: pasteTypes
    running: false
    command: ["wl-paste", "--list-types"]
    stdout: StdioCollector { waitForEnd: true; onStreamFinished: root.pasteTypesDone(text) }
  }

  Process {
    id: pasteUris
    running: false
    command: ["wl-paste", "--no-newline", "--type", "text/uri-list"]
    stdout: StdioCollector { waitForEnd: true; onStreamFinished: root.pasteUrisDone(text) }
  }

  Process {
    id: pasteImage
    running: false
    property string dest: ""
    onExited: function(exitCode) {
      if (exitCode === 0) root.addFiles([dest])
      else root.showToast("Could not paste the picture")
    }
  }

  // The client has no widget entry of its own; it borrows the bar widget's
  // settings (notifications, read receipts, binary path) from shell.json.
  FileView {
    path: Quickshell.env("HOME") + "/.config/omarchy/shell.json"
    watchChanges: true
    printErrors: false
    onFileChanged: reload()
    onLoaded: root.widgetSettings = Model.widgetSettings(text(), "megamvb.omawhats")
  }

  // Omarchy's own emoji list (its emoji picker's), with a short built-in one
  // if it is not there.
  FileView {
    path: (Quickshell.env("OMARCHY_PATH") || "/usr/share/omarchy") + "/shell/plugins/emojis/emojis.json"
    printErrors: false
    onLoaded: {
      var list = Model.parseEmojis(text())
      if (list.length > 0) root.emojiAll = list
    }
  }

  FileView {
    id: recentFile
    path: wa.dataDir + "/recent-emoji.json"
    atomicWrites: true
    printErrors: false
    onLoaded: root.recentEmoji = Model.parseRecent(text())
  }

  Timer {
    id: raiseTimer
    interval: 150
    onTriggered: root.raiseWindow()
  }

  Timer {
    interval: 30000
    running: root.opened
    repeat: true
    onTriggered: root.nowMs = Date.now()
  }

  PanelWindow {
    id: window
    visible: root.opened && !root.asWindow && !root.picking
    anchors { top: true; bottom: true; left: true; right: true }
    color: "transparent"
    WlrLayershell.namespace: "marcos-whatsapp"
    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.keyboardFocus: WlrKeyboardFocus.Exclusive
    exclusionMode: ExclusionMode.Ignore

    Rectangle { anchors.fill: parent; color: root.scrim }
    MouseArea { anchors.fill: parent; onClicked: root.dismiss() }
  }

  // The same client as an ordinary window: tiled, on a workspace, in the
  // window switcher. Shown and hidden by syncAppWindow(), not by a binding —
  // the compositor closing it (Super+W) sets visible itself.
  FloatingWindow {
    id: appWindow
    visible: false
    title: "OmaWhats"
    color: root.solidBackground
    implicitWidth: Style.space(1100)
    implicitHeight: Style.space(760)
    minimumSize: Qt.size(Style.space(640), Style.space(420))
    onVisibleChanged: if (!visible && root.opened && root.asWindow) root.dismiss()
  }

  // The client itself, in whichever of the two windows the mode uses.
  BorderSurface {
    id: card
    parent: root.asWindow ? appWindow.contentItem : window.contentItem
    width: root.asWindow ? parent.width : Math.min(Style.space(1100), parent.width - Style.gapsOut * 2)
    height: root.asWindow ? parent.height : Math.min(Style.space(760), parent.height - Style.gapsOut * 2)
    radius: root.asWindow ? 0 : Style.cornerRadius
    anchors.centerIn: parent
    color: root.asWindow ? root.solidBackground : root.background
    borderSpec: root.asWindow ? Border.none() : root.borderSpec
    padding: Style.spacing.panelPadding

    MouseArea { anchors.fill: parent; onClicked: {} }

    Item {
      id: content
      anchors.fill: parent
      anchors.topMargin: card.contentTopInset
      anchors.rightMargin: card.contentRightInset
      anchors.bottomMargin: card.contentBottomInset
      anchors.leftMargin: card.contentLeftInset
      focus: true
      Keys.onPressed: function(event) {
        if (root.handleKey(event)) event.accepted = true
        else if (event.key === Qt.Key_Escape) { root.escapeOut(); event.accepted = true }
      }

      RowLayout {
        anchors.fill: parent
        spacing: Style.space(14)

        // ================= conversations

        ColumnLayout {
          // Explicit on both ends: children that fill their width would
          // otherwise make this column claim half the card.
          readonly property real paneWidth: Math.min(Style.space(320), content.width * 0.38)
          Layout.preferredWidth: paneWidth
          Layout.maximumWidth: paneWidth
          Layout.fillWidth: false
          Layout.fillHeight: true
          spacing: Style.space(10)

          RowLayout {
            Layout.fillWidth: true
            spacing: Style.space(8)

            Text {
              text: Model.GLYPH_WHATSAPP
              color: Model.isAlarm(wa.daemonState, wa.live) ? Color.urgent : root.foreground
              opacity: wa.online ? 1.0 : 0.55
              font.family: root.fontFamily
              font.pixelSize: Style.font.display
            }

            ColumnLayout {
              Layout.fillWidth: true
              spacing: 0
              Text {
                Layout.fillWidth: true
                text: "OmaWhats"
                color: root.foreground
                font.family: root.fontFamily
                font.pixelSize: Style.font.heading
                font.bold: true
              }
              Text {
                Layout.fillWidth: true
                text: wa.loggingOut ? "Logging out…" : (wa.starting ? "Turning on…" : (wa.stopping ? "Turning off…" : Model.stateLabel(wa.daemonState, wa.live)))
                color: root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.caption
                elide: Text.ElideRight
              }
            }

            PanelActionButton {
              iconText: root.asWindow ? Model.GLYPH_POPUP : Model.GLYPH_WINDOW
              tooltipText: (root.asWindow ? "Back to the popup" : "Open in a window") + " (Ctrl+Shift+W)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: root.switchMode()
            }

            PanelActionButton {
              iconText: Model.GLYPH_KEYBOARD
              tooltipText: "Keyboard shortcuts (F1)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: root.openDialog("help")
            }

            PanelActionButton {
              iconText: Model.GLYPH_LOGOUT
              tooltipText: "Log out — unlink this computer (Ctrl+Shift+L)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              enabled: (wa.daemonState.paired || wa.live) && !wa.loggingOut
              onClicked: root.openDialog("logout")
            }

            PanelActionButton {
              iconText: Model.GLYPH_POWER
              tooltipText: (wa.live ? "Turn off" : "Turn on") + " (Ctrl+Shift+P)"
              foreground: wa.live ? root.accent : root.foreground
              fontFamily: root.fontFamily
              enabled: !wa.starting && !wa.stopping
              onClicked: wa.togglePower()
            }
          }

          RowLayout {
            Layout.fillWidth: true
            spacing: Style.space(6)

          TextField {
            id: search
            Layout.fillWidth: true
            placeholderText: "Search chats"
            foreground: root.foreground
            font.family: root.fontFamily
            onTextChanged: { root.query = text; root.listIndex = 0 }
            Keys.onPressed: function(event) {
              if (root.handleKey(event)) { event.accepted = true; return }
              if (event.key === Qt.Key_Down) { root.moveList(1); event.accepted = true }
              else if (event.key === Qt.Key_Up) { root.moveList(-1); event.accepted = true }
              else if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) { root.activateListIndex(); event.accepted = true }
              else if (event.key === Qt.Key_Tab) { composer.forceActiveFocus(); event.accepted = true }
              else if (event.key === Qt.Key_Escape) {
                if (text !== "") text = ""
                else root.escapeOut()
                event.accepted = true
              }
            }
          }

            PanelActionButton {
              iconText: Model.GLYPH_NEW_CHAT
              tooltipText: "New chat (Ctrl+N)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: root.openDialog("new")
            }
          }

          ListView {
            id: chatList
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true
            spacing: Style.space(3)
            model: root.filtered
            boundsBehavior: Flickable.StopAtBounds
            ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

            delegate: CursorSurface {
              id: chatRow
              required property var modelData
              required property int index
              readonly property int unreadCount: parseInt(modelData.unread, 10) || 0
              readonly property bool manualUnread: Model.isManualUnread(modelData)
              width: chatList.width - Style.space(8)
              implicitHeight: rowLayout.implicitHeight + Style.spacing.rowPaddingX
              foreground: root.foreground
              hasCursor: search.activeFocus && root.listIndex === index
              current: modelData.jid === root.currentChat

              // Right-click pins or unpins.
              MouseArea {
                anchors.fill: parent
                acceptedButtons: Qt.LeftButton | Qt.RightButton
                cursorShape: Qt.PointingHandCursor
                onClicked: function(mouse) {
                  if (mouse.button === Qt.RightButton) wa.togglePin(chatRow.modelData.jid)
                  else root.selectChat(chatRow.modelData.jid)
                }
              }

              RowLayout {
                id: rowLayout
                anchors.left: parent.left
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                anchors.leftMargin: Style.space(8)
                anchors.rightMargin: Style.space(8)
                spacing: Style.space(8)

                Text {
                  text: chatRow.modelData.group ? Model.GLYPH_GROUP : Model.GLYPH_PERSON
                  color: root.foreground
                  opacity: chatRow.unreadCount > 0 ? 1.0 : 0.5
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.icon
                }

                ColumnLayout {
                  Layout.fillWidth: true
                  spacing: Style.space(1)
                  Text {
                    Layout.fillWidth: true
                    text: Model.chatName(chatRow.modelData)
                    color: root.foreground
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.body
                    font.bold: chatRow.unreadCount > 0
                    elide: Text.ElideRight
                    maximumLineCount: 1
                  }
                  Text {
                    Layout.fillWidth: true
                    text: Model.chatPreview(chatRow.modelData)
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                    elide: Text.ElideRight
                    maximumLineCount: 1
                    textFormat: Text.PlainText
                  }
                }

                ColumnLayout {
                  spacing: Style.space(2)
                  Row {
                    Layout.alignment: Qt.AlignRight
                    spacing: Style.space(4)
                    Text {
                      visible: chatRow.modelData.pinned === true
                      text: Model.GLYPH_PIN
                      color: root.dim
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.caption
                    }
                  Text {
                    text: Model.listTime(chatRow.modelData.ts, root.nowMs)
                    color: chatRow.unreadCount > 0 ? root.accent : root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                  }
                  }
                  Rectangle {
                    Layout.alignment: Qt.AlignRight
                    visible: chatRow.unreadCount > 0
                    implicitWidth: Math.max(height, badgeText.implicitWidth + Style.space(8))
                    implicitHeight: badgeText.implicitHeight + Style.space(2)
                    radius: height / 2
                    color: root.accent
                    Text {
                      id: badgeText
                      anchors.centerIn: parent
                      // By hand: a dot, since the count behind it means nothing.
                      text: chatRow.manualUnread ? "" : Model.unreadBadge(chatRow.unreadCount)
                      color: Color.background
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.caption
                      font.bold: true
                    }
                  }
                }
              }
            }

            footer: CursorSurface {
              visible: root.newChatJid !== ""
              width: chatList.width - Style.space(8)
              implicitHeight: visible ? newChatText.implicitHeight + Style.spacing.rowPaddingX : 0
              foreground: root.foreground
              hasCursor: search.activeFocus && root.listIndex === root.filtered.length
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: { root.listIndex = root.filtered.length; root.activateListIndex() }
              }
              Text {
                id: newChatText
                anchors.verticalCenter: parent.verticalCenter
                anchors.left: parent.left
                anchors.leftMargin: Style.space(8)
                text: Model.GLYPH_PERSON + "  New chat with " + Model.formatPhone(root.newChatJid.split("@")[0])
                color: root.foreground
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
              }
            }

            Text {
              anchors.centerIn: parent
              width: parent.width - Style.space(20)
              visible: root.filtered.length === 0 && root.newChatJid === ""
              text: root.query !== "" ? "No chats found. Type a full phone number to start a new one."
                : (wa.live ? (wa.daemonState.syncing ? "Syncing history…" : "No chats yet.")
                           : "Nothing stored yet. Turn OmaWhats on to connect.")
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              wrapMode: Text.WordWrap
              horizontalAlignment: Text.AlignHCenter
            }
          }
        }

        Rectangle {
          Layout.fillHeight: true
          Layout.preferredWidth: 1
          color: Util.alpha(root.foreground, 0.15)
        }

        // ================= pairing (replaces the conversation while needed)

        ColumnLayout {
          visible: root.pairingView
          Layout.fillWidth: true
          Layout.fillHeight: true
          spacing: Style.space(14)

          Item { Layout.fillHeight: true }

          Text {
            Layout.alignment: Qt.AlignHCenter
            text: wa.daemonState.state === "pairing" ? "Link your WhatsApp" : Model.stateLabel(wa.daemonState, wa.live)
            color: root.foreground
            font.family: root.fontFamily
            font.pixelSize: Style.font.heading
            font.bold: true
          }

          QrCode {
            visible: wa.daemonState.state === "pairing"
            Layout.alignment: Qt.AlignHCenter
            Layout.preferredWidth: Math.min(Style.space(320), content.height * 0.55)
            Layout.preferredHeight: Layout.preferredWidth
            rows: wa.daemonState.qr
          }

          Text {
            Layout.alignment: Qt.AlignHCenter
            Layout.maximumWidth: Style.space(420)
            text: wa.daemonState.state === "pairing"
              ? "On your phone, open WhatsApp → Settings → Linked devices → Link a device, and point the camera at this code. It changes every ~20 seconds."
              : (wa.daemonState.error || "Generate a new QR code to link this computer (Ctrl+R).")
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            wrapMode: Text.WordWrap
            horizontalAlignment: Text.AlignHCenter
          }

          Button {
            visible: wa.daemonState.state !== "pairing"
            Layout.alignment: Qt.AlignHCenter
            iconText: Model.GLYPH_QR
            text: "Generate new QR code"
            foreground: root.foreground
            fontFamily: root.fontFamily
            onClicked: wa.pair()
          }

          Item { Layout.fillHeight: true }
        }

        // ================= conversation

        ColumnLayout {
          visible: !root.pairingView
          Layout.fillWidth: true
          Layout.fillHeight: true
          spacing: Style.space(8)

          // ---- header
          RowLayout {
            Layout.fillWidth: true
            visible: root.currentChat !== ""
            spacing: Style.space(10)

            Text {
              text: root.current && root.current.group ? Model.GLYPH_GROUP : Model.GLYPH_PERSON
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.display
            }

            ColumnLayout {
              Layout.fillWidth: true
              spacing: 0
              Text {
                Layout.fillWidth: true
                text: root.currentTitle
                color: root.foreground
                font.family: root.fontFamily
                font.pixelSize: Style.font.heading
                font.bold: true
                elide: Text.ElideRight
              }
              Text {
                Layout.fillWidth: true
                visible: text !== "" && text !== root.currentTitle
                text: root.currentSubtitle
                color: root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.caption
                elide: Text.ElideRight
              }
            }

            PanelActionButton {
              visible: root.currentPinned
              iconText: Model.GLYPH_UP
              tooltipText: "Move pin up (Alt+Shift+↑)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              enabled: wa.pins.indexOf(root.currentChat) > 0
              onClicked: root.moveCurrentPin(-1)
            }

            PanelActionButton {
              visible: root.currentPinned
              iconText: Model.GLYPH_DOWN
              tooltipText: "Move pin down (Alt+Shift+↓)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              enabled: wa.pins.indexOf(root.currentChat) < wa.pins.length - 1
              onClicked: root.moveCurrentPin(1)
            }

            PanelActionButton {
              iconText: Model.GLYPH_MARK_UNREAD
              tooltipText: "Mark as unread and close (Ctrl+U)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              enabled: wa.live
              onClicked: root.markUnreadCurrent()
            }

            PanelActionButton {
              iconText: root.currentPinned ? Model.GLYPH_UNPIN : Model.GLYPH_PIN
              tooltipText: (root.currentPinned ? "Unpin" : "Pin to the top") + " (Ctrl+P)"
              foreground: root.currentPinned ? root.accent : root.foreground
              fontFamily: root.fontFamily
              onClicked: root.togglePinCurrent()
            }

            PanelActionButton {
              iconText: Model.GLYPH_CLOSE
              tooltipText: "Close chat (Ctrl+W)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: root.closeChat()
            }
          }

          PanelSeparator {
            Layout.fillWidth: true
            visible: root.currentChat !== ""
            foreground: root.foreground
          }

          // ---- nothing selected
          Item {
            visible: root.currentChat === ""
            Layout.fillWidth: true
            Layout.fillHeight: true

            Column {
              anchors.centerIn: parent
              spacing: Style.space(10)
              Text {
                anchors.horizontalCenter: parent.horizontalCenter
                text: Model.GLYPH_WHATSAPP
                color: root.foreground
                opacity: 0.25
                font.family: root.fontFamily
                font.pixelSize: Style.font.displayLarge * 3
              }
              Text {
                anchors.horizontalCenter: parent.horizontalCenter
                text: wa.live || wa.chats.length > 0 ? "Pick a chat, or start a new one (Ctrl+N)" : "Turn OmaWhats on to connect (Ctrl+Shift+P)"
                color: root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
              }
              // Which halves are actually running: the plugin is reloaded by
              // the shell, the daemon only when it is turned off and on, so
              // the two can be of different ages without anything saying so.
              Text {
                anchors.horizontalCenter: parent.horizontalCenter
                text: Model.versionLine(root.manifest ? root.manifest.version : "", wa.daemonState, wa.live)
                color: root.dim
                opacity: 0.7
                font.family: root.fontFamily
                font.pixelSize: Style.font.caption
              }
            }
          }

          // ---- messages
          ListView {
            id: messageList
            visible: root.currentChat !== ""
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true
            spacing: Style.space(4)
            model: messages
            boundsBehavior: Flickable.StopAtBounds
            ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

            // Pictures load asynchronously and grow their rows after the
            // first positionViewAtEnd, so the view follows the end until the
            // reader scrolls away from it.
            //
            // Scrolling up creates rows above, which changes contentHeight
            // before anything else hears of the scroll; so the queued jump
            // re-checks `follow` when it runs instead of trusting the value
            // it was queued with, and any movement drops it at once.
            property bool follow: true
            onMovementStarted: follow = false
            onMovementEnded: follow = atYEnd
            onContentHeightChanged: if (follow) Qt.callLater(root.followEnd)

            onContentYChanged: if (!root.initialLoad) root.maybeLoadOlder()

            // Keyboard selection mode (Ctrl+Up from the message box).
            Keys.onPressed: function(event) {
              if (root.msgCursor < 0) {
                if (root.handleKey(event)) event.accepted = true
                return
              }
              var plain = (event.modifiers & (Qt.ControlModifier | Qt.AltModifier)) === 0
              if (plain && event.key === Qt.Key_Up) root.moveMessageCursor(-1)
              else if (plain && event.key === Qt.Key_Down) {
                if (root.msgCursor >= messages.count - 1) root.leaveMessageCursor()
                else root.moveMessageCursor(1)
              }
              else if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) root.activateMessage(root.msgCursor)
              else if (event.key === Qt.Key_C) root.copyMessage(root.msgCursor)
              else if (plain && event.key === Qt.Key_R) root.startReply(root.msgCursor)
              else if (plain && event.key === Qt.Key_E) root.openReactions(root.msgCursor)
              else if (event.key === Qt.Key_Space) {
                var sel = messages.get(root.msgCursor)
                var selMedia = Model.parseJson(sel.mediaJson)
                if (Model.isAudio(selMedia)) root.toggleAudio(sel.mid, selMedia)
              }
              else if (event.key === Qt.Key_Escape || event.key === Qt.Key_Tab) root.leaveMessageCursor()
              else if (event.key === Qt.Key_Home) { root.msgCursor = 0; messageList.positionViewAtIndex(0, ListView.Beginning); root.maybeLoadOlder() }
              else if (event.key === Qt.Key_End) { root.msgCursor = messages.count - 1; messageList.positionViewAtIndex(root.msgCursor, ListView.End) }
              else if (root.handleKey(event)) {}
              else if (event.text !== "" && plain) {
                // Typing goes back to the message box.
                root.leaveMessageCursor()
                composer.insert(composer.cursorPosition, event.text)
              }
              else return
              event.accepted = true
            }

            // Always the same height: a header that grows and shrinks would
            // shift the messages under the reader while pages load.
            header: Item {
              width: messageList.width
              height: Style.space(30)
              Text {
                anchors.centerIn: parent
                width: parent.width - Style.space(20)
                text: root.initialLoad ? "Loading…" : Model.historyHeader(root.historyPhase)
                color: root.historyPhase === "timeout" ? root.accent : root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.caption
                horizontalAlignment: Text.AlignHCenter
                elide: Text.ElideRight
              }
              MouseArea {
                anchors.fill: parent
                enabled: root.historyPhase === "timeout"
                cursorShape: enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
                onClicked: root.retryOlder()
              }
            }

            delegate: Column {
              id: msgItem
              required property int index
              required property string mid
              required property string senderName
              required property bool fromMe
              required property double ts
              required property string body
              required property string kind
              required property string status
              required property bool edited
              required property string error
              required property string mediaJson
              required property string linkJson
              required property string quoteJson
              required property string reactionsJson
              required property string mediaState

              readonly property var media: Model.parseJson(mediaJson)
              readonly property var link: Model.parseJson(linkJson)
              readonly property var quote: Model.parseJson(quoteJson)
              readonly property var reactionGroups: Model.reactionSummary(Model.parseJson(reactionsJson))
              readonly property bool actionable: status !== "pending" && status !== "failed" && kind !== "revoked" && mid.indexOf("pending-") !== 0
              readonly property bool visual: Model.isVisualMedia(media)
              readonly property bool audioRow: Model.isAudio(media)
              readonly property bool fileRow: !!media && !visual && !audioRow
              readonly property string shownText: Model.bubbleText(body, media)
              readonly property int emojiCount: media ? 0 : Model.emojiOnlyCount(shownText)
              // A sticker, like a big emoji, floats without a bubble.
              readonly property bool bare: ((media && media.type === "sticker" && shownText === "") || emojiCount > 0) && !quote
              readonly property bool mediaBusy: mediaState === "loading" || mediaState === "sending"
              readonly property string mediaError: mediaBusy ? "" : mediaState
              readonly property string busyLabel: mediaState === "sending" ? "sending…" : "downloading…"

              // The row above, or null; while a page is being inserted at the
              // top, index can briefly point past what the model holds.
              readonly property var prev: index > 0 && index - 1 < messages.count ? messages.get(index - 1) : null
              readonly property bool showDay: !prev || Model.differentDay(ts, prev.ts)
              readonly property bool showSender: !fromMe && !!root.current && root.current.group === true
                && (!prev || showDay || prev.senderName !== senderName || prev.fromMe)
              readonly property real maxBubble: messageList.width * 0.72
              readonly property var box: Model.mediaBox(media, maxBubble - Style.space(20), Style.space(320))

              width: messageList.width - Style.space(10)
              spacing: Style.space(4)

              // Where the bubble is, for placing the reaction bar next to it.
              function bubbleRect(target) { return bubble.mapToItem(target, 0, 0, bubble.width, bubble.height) }

              Component.onCompleted: root.autoFetch(mid, media)
              onMediaJsonChanged: root.autoFetch(mid, media)

              Item {
                visible: msgItem.showDay
                width: parent.width
                height: visible ? dayText.implicitHeight + Style.space(10) : 0
                Rectangle {
                  anchors.centerIn: parent
                  width: dayText.implicitWidth + Style.space(16)
                  height: dayText.implicitHeight + Style.space(4)
                  radius: height / 2
                  color: Util.alpha(root.foreground, 0.08)
                  Text {
                    id: dayText
                    anchors.centerIn: parent
                    text: Model.dayLabel(msgItem.ts, root.nowMs)
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                  }
                }
              }

              Item {
                width: parent.width
                // Reaction chips hang over the bubble's bottom edge.
                height: bubble.height + (chipRow.visible ? chipRow.height - Style.space(4) : 0)

                HoverHandler { id: rowHover }

                // Keyboard selection, or the quoted message just jumped to.
                Rectangle {
                  visible: root.msgCursor === msgItem.index || (root.flashId !== "" && root.flashId === msgItem.mid)
                  x: bubble.x - Style.space(3)
                  y: -Style.space(3)
                  width: bubble.width + Style.space(6)
                  height: bubble.height + Style.space(6)
                  radius: bubble.radius + Style.space(3)
                  color: "transparent"
                  border.width: Math.max(1, Style.space(2))
                  border.color: root.accent
                }

                Rectangle {
                  id: bubble
                  readonly property real pad: Style.space(10)
                  x: msgItem.fromMe ? parent.width - width : 0
                  width: Math.min(msgItem.maxBubble, Math.max(
                    bodyText.visible ? bodyText.implicitWidth : 0,
                    senderText.visible ? senderText.implicitWidth : 0,
                    quoteBox.visible ? quoteBox.implicitWidth : 0,
                    mediaBox.visible ? mediaBox.width : 0,
                    fileBox.visible ? fileBox.implicitWidth : 0,
                    audioBox.visible ? audioBox.implicitWidth : 0,
                    linkCard.visible ? Style.space(260) : 0,
                    metaRow.implicitWidth) + pad * 2)
                  height: bubbleColumn.implicitHeight + Style.space(12)
                  radius: Math.max(Style.cornerRadius, Style.space(8))
                  color: msgItem.bare ? "transparent"
                    : (msgItem.fromMe ? Util.alpha(root.accent, 0.22) : Util.alpha(root.foreground, 0.07))
                  border.width: msgItem.status === "failed" ? 1 : 0
                  border.color: Color.urgent

                  Column {
                    id: bubbleColumn
                    x: bubble.pad
                    y: Style.space(6)
                    width: bubble.width - bubble.pad * 2
                    spacing: Style.space(4)

                    Text {
                      id: senderText
                      visible: msgItem.showSender
                      width: parent.width
                      text: msgItem.senderName
                      color: root.accent
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.caption
                      font.bold: true
                      elide: Text.ElideRight
                    }

                    // ---- the message this one answers; a click scrolls to it
                    Rectangle {
                      id: quoteBox
                      visible: !!msgItem.quote
                      readonly property bool hasThumb: !!msgItem.quote && !!msgItem.quote.thumb && quoteThumb.status === Image.Ready
                      width: parent.width
                      implicitWidth: Math.min(msgItem.maxBubble - bubble.pad * 2,
                        Math.max(quoteName.implicitWidth, quoteText.implicitWidth) + Style.space(20) + (hasThumb ? Style.space(48) : 0))
                      height: Math.max(quoteColumn.implicitHeight, hasThumb ? Style.space(40) : 0) + Style.space(10)
                      radius: Style.space(6)
                      color: quoteMouse.containsMouse ? Util.alpha(root.foreground, 0.13) : Util.alpha(root.foreground, 0.07)
                      clip: true

                      Rectangle {
                        width: Style.space(3)
                        height: parent.height
                        color: root.accent
                      }

                      Column {
                        id: quoteColumn
                        x: Style.space(10)
                        anchors.verticalCenter: parent.verticalCenter
                        width: parent.width - Style.space(16) - (quoteBox.hasThumb ? Style.space(48) : 0)
                        spacing: Style.space(1)
                        Text {
                          id: quoteName
                          width: parent.width
                          visible: text !== ""
                          text: Model.quoteName(msgItem.quote, root.current && root.current.group ? "" : root.currentTitle)
                          color: root.accent
                          font.family: root.fontFamily
                          font.pixelSize: Style.font.caption
                          font.bold: true
                          elide: Text.ElideRight
                        }
                        Text {
                          id: quoteText
                          width: parent.width
                          text: Model.quoteLine(msgItem.quote)
                          color: root.dim
                          font.family: root.fontFamily
                          font.pixelSize: Style.font.bodySmall
                          textFormat: Text.PlainText
                          wrapMode: Text.Wrap
                          maximumLineCount: 2
                          elide: Text.ElideRight
                        }
                      }

                      Image {
                        id: quoteThumb
                        visible: quoteBox.hasThumb
                        anchors.right: parent.right
                        anchors.rightMargin: Style.space(5)
                        anchors.verticalCenter: parent.verticalCenter
                        width: Style.space(40)
                        height: Style.space(40)
                        source: msgItem.quote ? Model.fileUrl(msgItem.quote.thumb) : ""
                        fillMode: Image.PreserveAspectCrop
                        asynchronous: true
                        clip: true
                      }

                      MouseArea {
                        id: quoteMouse
                        anchors.fill: parent
                        hoverEnabled: true
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.jumpToMessage(msgItem.quote.id)
                      }
                    }

                    // ---- picture: photo, sticker, GIF, or a video's thumbnail
                    Item {
                      id: mediaBox
                      visible: msgItem.visual
                      width: msgItem.box.w
                      height: msgItem.box.h
                      anchors.horizontalCenter: msgItem.bare ? undefined : parent.horizontalCenter

                      readonly property string source: Model.mediaSource(msgItem.media)
                      readonly property bool animated: Model.mediaAnimated(msgItem.media)

                      Rectangle {
                        anchors.fill: parent
                        visible: !msgItem.bare
                        radius: Style.space(6)
                        color: Util.alpha(root.foreground, 0.06)
                      }

                      Image {
                        id: still
                        anchors.fill: parent
                        visible: !mediaBox.animated && status === Image.Ready
                        source: mediaBox.animated ? "" : mediaBox.source
                        fillMode: Image.PreserveAspectFit
                        asynchronous: true
                        cache: true
                        smooth: true
                        sourceSize.width: Math.round(mediaBox.width * 2)
                      }

                      AnimatedImage {
                        anchors.fill: parent
                        visible: mediaBox.animated
                        source: mediaBox.animated ? mediaBox.source : ""
                        playing: visible && root.opened
                        fillMode: Image.PreserveAspectFit
                        asynchronous: true
                        cache: false
                      }

                      Text {
                        anchors.centerIn: parent
                        visible: mediaBox.source === "" && !msgItem.mediaBusy
                        text: msgItem.media && msgItem.media.type === "sticker" ? "🏷" : "📷"
                        font.pixelSize: Style.font.display
                        opacity: 0.5
                      }

                      // Video: a play button over the thumbnail; the file
                      // opens in the system player.
                      Rectangle {
                        visible: msgItem.media && msgItem.media.type === "video"
                        anchors.centerIn: parent
                        width: Style.space(44)
                        height: width
                        radius: width / 2
                        color: Qt.rgba(0, 0, 0, 0.55)
                        Text {
                          anchors.centerIn: parent
                          anchors.horizontalCenterOffset: Style.space(2)
                          text: Model.GLYPH_PLAY
                          color: "white"
                          font.family: root.fontFamily
                          font.pixelSize: Style.font.display
                        }
                      }

                      // Corner badge: GIF, video length, download progress.
                      Rectangle {
                        readonly property string label: {
                          var m = msgItem.media
                          if (!m) return ""
                          if (msgItem.mediaBusy) return msgItem.busyLabel
                          if (m.type === "video") return (m.seconds ? Model.formatDuration(m.seconds) : "video") + (m.size ? " · " + Model.formatSize(m.size) : "")
                          if (m.type === "gif" && !m.anim) return "GIF"
                          return ""
                        }
                        visible: label !== ""
                        anchors.left: parent.left
                        anchors.bottom: parent.bottom
                        anchors.margins: Style.space(6)
                        width: badgeLabel.implicitWidth + Style.space(10)
                        height: badgeLabel.implicitHeight + Style.space(4)
                        radius: height / 2
                        color: Qt.rgba(0, 0, 0, 0.6)
                        Text {
                          id: badgeLabel
                          anchors.centerIn: parent
                          text: parent.label
                          color: "white"
                          font.family: root.fontFamily
                          font.pixelSize: Style.font.caption
                        }
                      }

                      MouseArea {
                        anchors.fill: parent
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.openMedia(msgItem.mid, msgItem.media)
                      }
                    }

                    // ---- audio or document: a row that opens the file
                    Rectangle {
                      id: fileBox
                      visible: msgItem.fileRow
                      width: parent.width
                      implicitWidth: fileRowLayout.implicitWidth + Style.space(16)
                      height: fileRowLayout.implicitHeight + Style.space(12)
                      radius: Style.space(6)
                      color: fileMouse.containsMouse ? Util.alpha(root.foreground, 0.1) : Util.alpha(root.foreground, 0.05)

                      RowLayout {
                        id: fileRowLayout
                        anchors.fill: parent
                        anchors.margins: Style.space(6)
                        anchors.leftMargin: Style.space(8)
                        spacing: Style.space(8)

                        Text {
                          text: msgItem.mediaBusy ? (msgItem.mediaState === "sending" ? Model.GLYPH_UPLOAD : Model.GLYPH_DOWNLOAD) : Model.fileRowGlyph(msgItem.media)
                          color: root.foreground
                          font.family: root.fontFamily
                          font.pixelSize: Style.font.display
                        }
                        ColumnLayout {
                          Layout.fillWidth: true
                          spacing: 0
                          Text {
                            Layout.fillWidth: true
                            Layout.maximumWidth: msgItem.maxBubble - Style.space(80)
                            text: Model.fileRowLabel(msgItem.media)
                            color: root.foreground
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.body
                            elide: Text.ElideMiddle
                          }
                          Text {
                            Layout.fillWidth: true
                            text: msgItem.mediaBusy ? msgItem.busyLabel : Model.fileRowDetail(msgItem.media)
                            color: root.dim
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.caption
                            elide: Text.ElideRight
                          }
                        }
                      }

                      MouseArea {
                        id: fileMouse
                        anchors.fill: parent
                        hoverEnabled: true
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.openMedia(msgItem.mid, msgItem.media)
                      }
                    }

                    // ---- audio: played right here
                    Rectangle {
                      id: audioBox
                      readonly property bool active: root.audioId === msgItem.mid
                      readonly property bool playing: active && audioPlayer.playbackState === MediaPlayer.PlayingState
                      readonly property real durMs: active && audioPlayer.duration > 0 ? audioPlayer.duration
                        : (msgItem.media ? (Number(msgItem.media.seconds) || 0) * 1000 : 0)
                      readonly property real posMs: active ? audioPlayer.position : 0

                      visible: msgItem.audioRow
                      width: parent.width
                      implicitWidth: Style.space(300)
                      height: audioRowLayout.implicitHeight + Style.space(12)
                      radius: Style.space(6)
                      color: Util.alpha(root.foreground, active ? 0.09 : 0.05)

                      RowLayout {
                        id: audioRowLayout
                        anchors.fill: parent
                        anchors.margins: Style.space(6)
                        anchors.leftMargin: Style.space(8)
                        spacing: Style.space(10)

                        Rectangle {
                          Layout.preferredWidth: Style.space(36)
                          Layout.preferredHeight: Style.space(36)
                          radius: width / 2
                          color: playMouse.containsMouse ? Util.alpha(root.accent, 0.45) : Util.alpha(root.accent, 0.28)
                          Text {
                            anchors.centerIn: parent
                            anchors.horizontalCenterOffset: !audioBox.playing && !msgItem.mediaBusy ? Style.space(1) : 0
                            text: msgItem.mediaBusy ? Model.GLYPH_DOWNLOAD : (audioBox.playing ? Model.GLYPH_PAUSE : Model.GLYPH_PLAY)
                            color: root.foreground
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.icon
                          }
                          MouseArea {
                            id: playMouse
                            anchors.fill: parent
                            hoverEnabled: true
                            cursorShape: Qt.PointingHandCursor
                            onClicked: root.toggleAudio(msgItem.mid, msgItem.media)
                          }
                        }

                        ColumnLayout {
                          Layout.fillWidth: true
                          spacing: Style.space(2)

                          RowLayout {
                            Layout.fillWidth: true
                            spacing: Style.space(6)
                            Text {
                              text: msgItem.media && msgItem.media.voice ? Model.GLYPH_MIC : Model.GLYPH_MUSIC
                              color: root.dim
                              font.family: root.fontFamily
                              font.pixelSize: Style.font.caption
                            }
                            Text {
                              Layout.fillWidth: true
                              text: Model.audioLabel(msgItem.media)
                              color: root.foreground
                              font.family: root.fontFamily
                              font.pixelSize: Style.font.bodySmall
                              elide: Text.ElideMiddle
                            }
                          }

                          // Progress; click anywhere on it to jump there.
                          Item {
                            Layout.fillWidth: true
                            Layout.preferredHeight: Style.space(14)
                            Rectangle {
                              id: track
                              anchors.verticalCenter: parent.verticalCenter
                              width: parent.width
                              height: Style.space(4)
                              radius: height / 2
                              color: Util.alpha(root.foreground, 0.18)
                            }
                            Rectangle {
                              anchors.verticalCenter: parent.verticalCenter
                              width: audioBox.durMs > 0 ? track.width * Math.min(1, audioBox.posMs / audioBox.durMs) : 0
                              height: track.height
                              radius: track.radius
                              color: root.accent
                            }
                            Rectangle {
                              visible: audioBox.active
                              anchors.verticalCenter: parent.verticalCenter
                              width: Style.space(10)
                              height: width
                              radius: width / 2
                              x: (audioBox.durMs > 0 ? track.width * Math.min(1, audioBox.posMs / audioBox.durMs) : 0) - width / 2
                              color: root.accent
                            }
                            MouseArea {
                              anchors.fill: parent
                              cursorShape: Qt.PointingHandCursor
                              onClicked: function(mouse) { root.seekAudio(msgItem.mid, msgItem.media, mouse.x / width) }
                            }
                          }

                          Text {
                            text: msgItem.mediaBusy ? "downloading…" : Model.audioTime(audioBox.posMs, audioBox.durMs, audioBox.active)
                            color: root.dim
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.caption
                          }
                        }

                        // Speed, shown on the audio that is loaded.
                        Rectangle {
                          visible: audioBox.active
                          Layout.preferredWidth: rateText.implicitWidth + Style.space(12)
                          Layout.preferredHeight: rateText.implicitHeight + Style.space(6)
                          radius: height / 2
                          color: rateMouse.containsMouse ? Util.alpha(root.foreground, 0.2) : Util.alpha(root.foreground, 0.1)
                          Text {
                            id: rateText
                            anchors.centerIn: parent
                            text: Model.rateLabel(root.audioRate)
                            color: root.foreground
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.caption
                            font.bold: true
                          }
                          MouseArea {
                            id: rateMouse
                            anchors.fill: parent
                            hoverEnabled: true
                            cursorShape: Qt.PointingHandCursor
                            onClicked: root.cycleAudioRate()
                          }
                        }
                      }
                    }

                    Text {
                      visible: msgItem.mediaError !== ""
                      width: parent.width
                      text: Model.GLYPH_WARNING + " " + msgItem.mediaError
                      color: Color.urgent
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.caption
                      wrapMode: Text.WordWrap
                    }

                    // ---- link preview
                    Rectangle {
                      id: linkCard
                      visible: !!msgItem.link
                      width: parent.width
                      height: linkRow.implicitHeight + Style.space(12)
                      radius: Style.space(6)
                      color: linkMouse.containsMouse ? Util.alpha(root.foreground, 0.1) : Util.alpha(root.foreground, 0.05)

                      Rectangle {
                        width: Style.space(3)
                        height: parent.height
                        radius: width / 2
                        color: root.accent
                      }

                      RowLayout {
                        id: linkRow
                        anchors.left: parent.left
                        anchors.right: parent.right
                        anchors.verticalCenter: parent.verticalCenter
                        anchors.leftMargin: Style.space(10)
                        anchors.rightMargin: Style.space(8)
                        spacing: Style.space(8)

                        Image {
                          visible: !!msgItem.link && !!msgItem.link.thumb && status === Image.Ready
                          source: msgItem.link ? Model.fileUrl(msgItem.link.thumb) : ""
                          Layout.preferredWidth: Style.space(52)
                          Layout.preferredHeight: Style.space(52)
                          fillMode: Image.PreserveAspectCrop
                          asynchronous: true
                          clip: true
                        }

                        ColumnLayout {
                          Layout.fillWidth: true
                          spacing: Style.space(1)
                          Text {
                            Layout.fillWidth: true
                            visible: text !== ""
                            text: msgItem.link ? String(msgItem.link.title || "") : ""
                            color: root.foreground
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.bodySmall
                            font.bold: true
                            wrapMode: Text.WordWrap
                            maximumLineCount: 2
                            elide: Text.ElideRight
                          }
                          Text {
                            Layout.fillWidth: true
                            visible: text !== ""
                            text: msgItem.link ? String(msgItem.link.description || "") : ""
                            color: root.dim
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.caption
                            wrapMode: Text.WordWrap
                            maximumLineCount: 2
                            elide: Text.ElideRight
                          }
                          Text {
                            Layout.fillWidth: true
                            text: Model.GLYPH_LINK + " " + (msgItem.link ? String(msgItem.link.url || "").replace(/^https?:\/\//, "") : "")
                            color: root.accent
                            font.family: root.fontFamily
                            font.pixelSize: Style.font.caption
                            elide: Text.ElideRight
                          }
                        }
                      }

                      MouseArea {
                        id: linkMouse
                        anchors.fill: parent
                        hoverEnabled: true
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.openLink(Model.firstLink("", msgItem.link))
                      }
                    }

                    // Rich text only to make links clickable; everything else
                    // is escaped first. TextEdit so it can be selected and copied.
                    TextEdit {
                      id: bodyText
                      visible: msgItem.shownText !== ""
                      width: parent.width
                      text: Model.linkify(msgItem.shownText, root.linkColor)
                      readOnly: true
                      selectByMouse: true
                      textFormat: TextEdit.RichText
                      wrapMode: TextEdit.Wrap
                      color: root.foreground
                      opacity: msgItem.kind === "revoked" ? 0.55 : 1.0
                      font.italic: msgItem.kind === "revoked" || (!msgItem.media && msgItem.kind !== "text")
                      font.family: root.fontFamily
                      font.pixelSize: msgItem.emojiCount > 0
                        ? Style.font.display * (msgItem.emojiCount === 1 ? 2.0 : 1.5)
                        : Style.font.body
                      selectionColor: Style.selectionFill
                      onLinkActivated: function(link) { root.openLink(link) }

                      HoverHandler {
                        cursorShape: bodyText.hoveredLink !== "" ? Qt.PointingHandCursor : Qt.IBeamCursor
                      }
                    }

                    Row {
                      id: metaRow
                      anchors.right: parent.right
                      spacing: Style.space(4)

                      Text {
                        visible: msgItem.edited
                        text: "edited"
                        color: root.dim
                        font.family: root.fontFamily
                        font.pixelSize: Style.font.caption
                      }
                      Text {
                        text: Model.clockTime(msgItem.ts)
                        color: root.dim
                        font.family: root.fontFamily
                        font.pixelSize: Style.font.caption
                      }
                      Text {
                        visible: msgItem.fromMe
                        text: Model.statusGlyph(msgItem.status)
                        color: msgItem.status === "read" ? root.accent
                             : (msgItem.status === "failed" ? Color.urgent : root.dim)
                        font.family: root.fontFamily
                        font.pixelSize: Style.font.caption
                        font.bold: msgItem.status === "failed"
                      }
                    }
                  }
                }

                // ---- reactions: one chip per emoji; a click gives (or
                // takes back) the same one
                Row {
                  id: chipRow
                  visible: msgItem.reactionGroups.length > 0
                  x: msgItem.fromMe ? bubble.x + bubble.width - width - Style.space(10) : bubble.x + Style.space(10)
                  y: bubble.height - Style.space(4)
                  spacing: Style.space(4)

                  Repeater {
                    model: msgItem.reactionGroups
                    Rectangle {
                      id: chip
                      required property var modelData
                      width: chipText.implicitWidth + Style.space(14)
                      height: chipText.implicitHeight + Style.space(4)
                      radius: height / 2
                      color: root.solidBackground
                      border.width: Math.max(1, Style.space(1))
                      border.color: modelData.mine ? root.accent : Util.alpha(root.foreground, 0.22)

                      Rectangle {
                        anchors.fill: parent
                        radius: parent.radius
                        color: chip.modelData.mine ? Util.alpha(root.accent, chipMouse.containsMouse ? 0.35 : 0.22)
                          : Util.alpha(root.foreground, chipMouse.containsMouse ? 0.14 : 0.07)
                      }
                      Text {
                        id: chipText
                        anchors.centerIn: parent
                        text: chip.modelData.emoji + (chip.modelData.count > 1 ? " " + chip.modelData.count : "")
                        color: root.foreground
                        font.family: root.fontFamily
                        font.pixelSize: Style.font.body
                      }
                      MouseArea {
                        id: chipMouse
                        anchors.fill: parent
                        hoverEnabled: true
                        cursorShape: msgItem.actionable ? Qt.PointingHandCursor : Qt.ArrowCursor
                        onClicked: if (msgItem.actionable) root.react(msgItem.mid, chip.modelData.emoji)
                      }
                      ToolTip.visible: chipMouse.containsMouse
                      ToolTip.text: chip.modelData.names.join(", ")
                      ToolTip.delay: 300
                    }
                  }
                }

                // ---- react / reply, shown beside the bubble under the pointer
                Row {
                  visible: rowHover.hovered && msgItem.actionable && root.dialog === ""
                  x: msgItem.fromMe ? bubble.x - width - Style.space(6) : bubble.x + bubble.width + Style.space(6)
                  y: Math.max(0, Math.min(Style.space(2), bubble.height - height))
                  spacing: Style.space(2)
                  MessageAction { glyph: Model.GLYPH_EMOJI; tip: "React"; onClicked: root.openReactions(msgItem.index) }
                  MessageAction { glyph: Model.GLYPH_REPLY; tip: "Reply"; onClicked: root.startReply(msgItem.index) }
                }
              }
            }
          }

          Text {
            id: sendError
            Layout.fillWidth: true
            visible: text !== ""
            color: Color.urgent
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            wrapMode: Text.WordWrap
          }

          // ---- replying to (click it to see the message)
          Rectangle {
            id: replyBar
            Layout.fillWidth: true
            visible: root.replyTo !== null && root.currentChat !== ""
            implicitHeight: replyRow.implicitHeight + Style.space(10)
            radius: Style.space(6)
            color: Util.alpha(root.foreground, 0.06)
            clip: true

            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: if (root.replyTo) root.jumpToMessage(root.replyTo.id)
            }
            Rectangle {
              width: Style.space(3)
              height: parent.height
              color: root.accent
            }
            RowLayout {
              id: replyRow
              anchors.left: parent.left
              anchors.right: parent.right
              anchors.verticalCenter: parent.verticalCenter
              anchors.leftMargin: Style.space(12)
              anchors.rightMargin: Style.space(4)
              spacing: Style.space(8)

              Text {
                text: Model.GLYPH_REPLY
                color: root.accent
                font.family: root.fontFamily
                font.pixelSize: Style.font.icon
              }
              ColumnLayout {
                Layout.fillWidth: true
                spacing: 0
                Text {
                  Layout.fillWidth: true
                  text: root.replyTo ? "Replying to " + (root.replyTo.fromMe ? "yourself" : (root.replyTo.name || "this message")) : ""
                  color: root.accent
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.caption
                  font.bold: true
                  elide: Text.ElideRight
                }
                Text {
                  Layout.fillWidth: true
                  text: root.replyTo ? Model.quoteLine(root.replyTo) : ""
                  color: root.dim
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.bodySmall
                  textFormat: Text.PlainText
                  elide: Text.ElideRight
                  maximumLineCount: 1
                }
              }
              Image {
                visible: !!root.replyTo && root.replyTo.thumb !== "" && status === Image.Ready
                source: root.replyTo ? Model.fileUrl(root.replyTo.thumb) : ""
                Layout.preferredWidth: Style.space(36)
                Layout.preferredHeight: Style.space(36)
                fillMode: Image.PreserveAspectCrop
                asynchronous: true
                clip: true
              }
              PanelActionButton {
                iconText: Model.GLYPH_CLOSE
                tooltipText: "Cancel the reply (Esc)"
                foreground: root.foreground
                fontFamily: root.fontFamily
                onClicked: { root.cancelReply(); composer.forceActiveFocus() }
              }
            }
          }

          // ---- files waiting to be sent (Enter sends them, Esc removes them)
          Rectangle {
            id: attachTray
            Layout.fillWidth: true
            visible: root.attachments.length > 0 && root.currentChat !== ""
            implicitHeight: trayColumn.implicitHeight + Style.space(14)
            radius: Style.space(6)
            color: Util.alpha(root.foreground, 0.06)

            readonly property bool hasPhotos: Model.attachmentsSummary(root.attachments, false).indexOf("photo") >= 0

            ColumnLayout {
              id: trayColumn
              anchors.left: parent.left
              anchors.right: parent.right
              anchors.verticalCenter: parent.verticalCenter
              anchors.leftMargin: Style.space(10)
              anchors.rightMargin: Style.space(4)
              spacing: Style.space(6)

              RowLayout {
                Layout.fillWidth: true
                spacing: Style.space(8)

                Text {
                  text: Model.GLYPH_ATTACH
                  color: root.accent
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.icon
                }
                Text {
                  Layout.fillWidth: true
                  text: "Sending " + Model.attachmentsSummary(root.attachments, root.attachAsFiles)
                    + (composer.text.trim() !== "" ? " · the message goes as the caption" : " · Enter sends")
                  color: root.foreground
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.bodySmall
                  elide: Text.ElideRight
                }
                // Photos go compressed, like WhatsApp does; as files they
                // arrive untouched.
                Rectangle {
                  visible: attachTray.hasPhotos
                  implicitWidth: asFilesLabel.implicitWidth + Style.space(16)
                  implicitHeight: asFilesLabel.implicitHeight + Style.space(6)
                  radius: height / 2
                  color: root.attachAsFiles ? Util.alpha(root.accent, 0.18) : (asFilesMouse.containsMouse ? Util.alpha(root.foreground, 0.08) : "transparent")
                  border.width: Math.max(1, Style.space(1))
                  border.color: root.attachAsFiles ? root.accent : Util.alpha(root.foreground, 0.25)
                  Text {
                    id: asFilesLabel
                    anchors.centerIn: parent
                    text: (root.attachAsFiles ? "✓ " : "") + "Photos as files (Ctrl+D)"
                    color: root.attachAsFiles ? root.accent : root.foreground
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                  }
                  MouseArea {
                    id: asFilesMouse
                    anchors.fill: parent
                    hoverEnabled: true
                    cursorShape: Qt.PointingHandCursor
                    onClicked: { root.toggleAttachAsFiles(); composer.forceActiveFocus() }
                  }
                }
                PanelActionButton {
                  iconText: Model.GLYPH_CLOSE
                  tooltipText: "Remove them all (Esc)"
                  foreground: root.foreground
                  fontFamily: root.fontFamily
                  onClicked: { root.clearAttachments(); composer.forceActiveFocus() }
                }
              }

              ListView {
                id: trayList
                Layout.fillWidth: true
                Layout.preferredHeight: Style.space(64)
                orientation: ListView.Horizontal
                spacing: Style.space(6)
                clip: true
                boundsBehavior: Flickable.StopAtBounds
                model: root.attachments

                delegate: Rectangle {
                  id: chip
                  required property var modelData
                  required property int index
                  readonly property bool asPhoto: modelData.photo && !root.attachAsFiles
                  width: asPhoto ? height : Style.space(210)
                  height: trayList.height
                  radius: Style.space(6)
                  color: Util.alpha(root.foreground, 0.08)
                  clip: true

                  Image {
                    anchors.fill: parent
                    visible: chip.asPhoto
                    source: chip.asPhoto ? Model.fileUrl(chip.modelData.path) : ""
                    sourceSize: Qt.size(Style.space(128), Style.space(128))
                    fillMode: Image.PreserveAspectCrop
                    asynchronous: true
                  }

                  RowLayout {
                    visible: !chip.asPhoto
                    anchors.fill: parent
                    anchors.leftMargin: Style.space(8)
                    anchors.rightMargin: Style.space(22)
                    spacing: Style.space(6)
                    Text {
                      text: chip.modelData.photo ? Model.GLYPH_FILE_IMAGE : Model.GLYPH_DOCUMENT
                      color: root.foreground
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.display
                    }
                    ColumnLayout {
                      Layout.fillWidth: true
                      spacing: 0
                      Text {
                        Layout.fillWidth: true
                        text: chip.modelData.name
                        color: root.foreground
                        font.family: root.fontFamily
                        font.pixelSize: Style.font.bodySmall
                        elide: Text.ElideMiddle
                      }
                      Text {
                        Layout.fillWidth: true
                        text: Model.formatSize(chip.modelData.size)
                        color: root.dim
                        font.family: root.fontFamily
                        font.pixelSize: Style.font.caption
                      }
                    }
                  }

                  // Remove this one.
                  Rectangle {
                    anchors.top: parent.top
                    anchors.right: parent.right
                    anchors.margins: Style.space(3)
                    width: Style.space(18)
                    height: width
                    radius: width / 2
                    color: removeMouse.containsMouse ? Color.urgent : Qt.rgba(0, 0, 0, 0.6)
                    Text {
                      anchors.centerIn: parent
                      text: Model.GLYPH_CLOSE
                      color: "white"
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.caption
                    }
                    MouseArea {
                      id: removeMouse
                      anchors.fill: parent
                      hoverEnabled: true
                      cursorShape: Qt.PointingHandCursor
                      onClicked: root.removeAttachment(chip.index)
                    }
                  }
                }
              }
            }
          }

          // ---- composer
          RowLayout {
            Layout.fillWidth: true
            visible: root.currentChat !== ""
            spacing: Style.space(8)

            PanelActionButton {
              iconText: Model.GLYPH_ATTACH
              tooltipText: "Attach photos or files (Ctrl+O)"
              foreground: root.picking ? root.accent : root.foreground
              fontFamily: root.fontFamily
              onClicked: root.pickFiles()
            }

            PanelActionButton {
              iconText: Model.GLYPH_EMOJI
              tooltipText: "Emoji (Ctrl+E)"
              foreground: root.dialog === "emoji" && root.pickerFor === "composer" ? root.accent : root.foreground
              fontFamily: root.fontFamily
              onClicked: root.dialog === "emoji" ? root.closeDialog() : root.openEmojiPicker("composer")
            }

            TextField {
              id: composer
              Layout.fillWidth: true
              enabled: root.currentChat !== ""
              placeholderText: !wa.canSend ? (wa.live ? "Waiting for the connection…" : "Off — turn OmaWhats on to send")
                : (root.attachments.length > 0 ? "Caption (optional) — Enter sends" : "Message")
              foreground: root.foreground
              font.family: root.fontFamily
              Keys.onPressed: function(event) {
                if (root.handleKey(event)) { event.accepted = true; return }
                if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) { root.sendComposer(); event.accepted = true }
                else if (event.key === Qt.Key_Tab) { search.forceActiveFocus(); event.accepted = true }
                else if (event.matches(StandardKey.Paste)) { root.pasteClipboard(); event.accepted = true }
                else if (event.key === Qt.Key_Escape) {
                  if (root.attachments.length > 0) root.clearAttachments()
                  else if (root.replyTo) root.cancelReply()
                  else root.escapeOut()
                  event.accepted = true
                }
              }
            }

            PanelActionButton {
              iconText: Model.GLYPH_SEND
              tooltipText: "Send (Enter)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              enabled: wa.canSend && (composer.text.trim() !== "" || root.attachments.length > 0)
              onClicked: root.sendComposer()
            }
          }

          Text {
            Layout.fillWidth: true
            text: "Enter sends · Ctrl+O attach · Ctrl+E emoji · Ctrl+↑ selects messages · F1 shortcuts"
            elide: Text.ElideMiddle
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            horizontalAlignment: Text.AlignHCenter
          }
        }
      }
    }

    // ---- files dropped on the client (from a file manager) are attached
    DropArea {
      id: dropZone
      anchors.fill: parent
      z: 8
      keys: ["text/uri-list"]
      onEntered: function(drag) {
        if (!drag.hasUrls) drag.accepted = false
      }
      onDropped: function(drop) {
        var urls = []
        for (var i = 0; i < drop.urls.length; i++) urls.push(String(drop.urls[i]))
        var paths = Model.pathsFromUris(urls)
        if (paths.length === 0) return
        drop.acceptProposedAction()
        root.addFiles(paths)
      }

      Rectangle {
        anchors.fill: parent
        anchors.margins: Style.space(12)
        visible: dropZone.containsDrag
        radius: Style.cornerRadius
        color: Util.alpha(root.solidBackground, 0.85)
        border.width: Math.max(2, Style.space(2))
        border.color: root.accent
        Text {
          anchors.centerIn: parent
          width: parent.width - Style.space(40)
          horizontalAlignment: Text.AlignHCenter
          wrapMode: Text.WordWrap
          text: root.currentChat !== "" ? Model.GLYPH_ATTACH + "  Drop to attach to " + root.currentTitle : "Open a chat first"
          color: root.currentChat !== "" ? root.accent : root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.display
        }
      }
    }

    // ---- toast ("Copied", …)
    Rectangle {
      visible: root.toastText !== ""
      z: 5
      anchors.horizontalCenter: parent.horizontalCenter
      anchors.bottom: parent.bottom
      anchors.bottomMargin: Style.space(root.replyTo ? 140 : 80) + (attachTray.visible ? attachTray.height + Style.space(8) : 0)
      width: toastLabel.implicitWidth + Style.space(28)
      height: toastLabel.implicitHeight + Style.space(12)
      radius: height / 2
      color: Util.alpha(root.foreground, 0.92)
      Text {
        id: toastLabel
        anchors.centerIn: parent
        text: root.toastText
        color: root.background
        font.family: root.fontFamily
        font.pixelSize: Style.font.bodySmall
      }
    }

    // ---- reaction bar and emoji picker, floating over the conversation
    Item {
      id: popupLayer
      anchors.fill: parent
      visible: root.dialog === "react" || root.dialog === "emoji"
      z: 9
      property real barX: 0
      property real barY: 0
      property real pickX: 0
      property real pickY: 0

      // A click anywhere else closes them.
      MouseArea { anchors.fill: parent; onClicked: root.closeDialog() }

      Rectangle {
        id: reactBar
        visible: root.dialog === "react"
        x: popupLayer.barX
        y: popupLayer.barY
        implicitWidth: reactRow.implicitWidth + Style.space(12)
        implicitHeight: reactRow.implicitHeight + Style.space(10)
        width: implicitWidth
        height: implicitHeight
        radius: height / 2
        color: root.solidBackground
        border.width: Math.max(1, Style.space(1))
        border.color: Util.alpha(root.foreground, 0.25)

        readonly property int choices: Model.QUICK_REACTIONS.length + 1

        MouseArea { anchors.fill: parent; onClicked: {} }

        Keys.onPressed: function(event) {
          var k = event.key
          var n = Model.QUICK_REACTIONS.length
          if (k === Qt.Key_Escape) root.closeDialog()
          else if (k === Qt.Key_Left || k === Qt.Key_Backtab) root.reactCursor = (root.reactCursor + reactBar.choices - 1) % reactBar.choices
          else if (k === Qt.Key_Right) root.reactCursor = (root.reactCursor + 1) % reactBar.choices
          else if (k >= Qt.Key_1 && k < Qt.Key_1 + n) root.pickReaction(Model.QUICK_REACTIONS[k - Qt.Key_1])
          else if (k === Qt.Key_Plus || k === Qt.Key_Equal || k === Qt.Key_Tab) root.openEmojiPicker("react")
          else if (k === Qt.Key_Return || k === Qt.Key_Enter || k === Qt.Key_Space) {
            if (root.reactCursor < n) root.pickReaction(Model.QUICK_REACTIONS[root.reactCursor])
            else root.openEmojiPicker("react")
          }
          else return
          event.accepted = true
        }

        Row {
          id: reactRow
          anchors.centerIn: parent
          spacing: Style.space(2)

          Repeater {
            model: Model.QUICK_REACTIONS.concat(["+"])
            Rectangle {
              id: reactChoice
              required property var modelData
              required property int index
              readonly property bool more: index === Model.QUICK_REACTIONS.length
              width: Style.space(42)
              height: width
              radius: width / 2
              color: index === root.reactCursor ? Util.alpha(root.accent, 0.4)
                : (!more && modelData === root.reactMine ? Util.alpha(root.accent, 0.2)
                   : (choiceMouse.containsMouse ? Util.alpha(root.foreground, 0.12) : "transparent"))
              Text {
                anchors.centerIn: parent
                text: reactChoice.modelData
                color: root.foreground
                font.family: root.fontFamily
                font.pixelSize: reactChoice.more ? Style.font.heading : Style.font.display
                font.bold: reactChoice.more
              }
              MouseArea {
                id: choiceMouse
                anchors.fill: parent
                hoverEnabled: true
                cursorShape: Qt.PointingHandCursor
                onClicked: reactChoice.more ? root.openEmojiPicker("react") : root.pickReaction(reactChoice.modelData)
              }
              ToolTip.visible: choiceMouse.containsMouse
              ToolTip.text: reactChoice.more ? "Any other emoji (+)"
                : ((reactChoice.modelData === root.reactMine ? "Take back" : "React") + " (" + (reactChoice.index + 1) + ")")
              ToolTip.delay: 500
            }
          }
        }
      }

      Rectangle {
        id: pickerPanel
        visible: root.dialog === "emoji"
        x: popupLayer.pickX
        y: popupLayer.pickY
        width: Math.min(Style.space(400), popupLayer.width - Style.space(16))
        height: Math.min(Style.space(380), popupLayer.height - Style.space(16))
        radius: Math.max(Style.cornerRadius, Style.space(8))
        color: root.solidBackground
        border.width: Math.max(1, Style.space(1))
        border.color: Util.alpha(root.foreground, 0.25)

        MouseArea { anchors.fill: parent; onClicked: {} }

        // Typing searches; there is no text box to click into.
        Item {
          id: pickerKeys
          anchors.fill: parent
          Keys.onPressed: function(event) {
            var k = event.key
            var mods = event.modifiers & (Qt.ControlModifier | Qt.AltModifier | Qt.MetaModifier)
            if (k === Qt.Key_Escape) {
              if (root.pickerQuery !== "") root.pickerQuery = ""
              else root.closeDialog()
            }
            else if (Util.editsFilter(event, root.pickerQuery)) root.pickerQuery = Util.editedFilter(event, root.pickerQuery)
            else if (k === Qt.Key_Backspace) {}
            else if (k === Qt.Key_Left) root.movePicker(-1)
            else if (k === Qt.Key_Right) root.movePicker(1)
            else if (k === Qt.Key_Up) root.movePicker(-root.pickerColumns)
            else if (k === Qt.Key_Down) root.movePicker(root.pickerColumns)
            else if (k === Qt.Key_PageUp) root.movePicker(-root.pickerColumns * 6)
            else if (k === Qt.Key_PageDown) root.movePicker(root.pickerColumns * 6)
            else if (k === Qt.Key_Return || k === Qt.Key_Enter) {
              var item = root.pickerItems[root.pickerIndex]
              if (item && item.e) root.pickEmoji(item.e)
            }
            else if (k === Qt.Key_E && mods === Qt.ControlModifier) root.closeDialog()
            else if (mods === 0 && event.text && event.text.length === 1 && event.text.charCodeAt(0) >= 32 && event.text.charCodeAt(0) !== 127)
              root.pickerQuery = root.pickerQuery + event.text
            else return
            event.accepted = true
          }
        }

        ColumnLayout {
          anchors.fill: parent
          anchors.margins: Style.space(10)
          spacing: Style.space(8)

          RowLayout {
            Layout.fillWidth: true
            spacing: Style.space(8)
            Text {
              text: Model.GLYPH_SEARCH
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.icon
            }
            Text {
              Layout.fillWidth: true
              text: root.pickerQuery !== "" ? root.pickerQuery
                : (root.pickerFor === "react" ? "React with… type to search" : "Type to search emojis")
              color: root.foreground
              opacity: root.pickerQuery !== "" ? 1.0 : 0.55
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              elide: Text.ElideLeft
            }
            Text {
              text: "Enter picks · Esc " + (root.pickerQuery !== "" ? "clears" : "closes")
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
            }
          }

          GridView {
            id: pickerGrid
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true
            model: root.pickerItems
            cellWidth: Math.floor(width / root.pickerColumns)
            cellHeight: Style.space(42)
            boundsBehavior: Flickable.StopAtBounds
            ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

            delegate: Rectangle {
              id: emojiCell
              required property var modelData
              required property int index
              width: pickerGrid.cellWidth
              height: pickerGrid.cellHeight
              radius: Style.space(6)
              color: modelData.e !== "" && index === root.pickerIndex ? Util.alpha(root.accent, 0.35) : "transparent"
              Text {
                anchors.centerIn: parent
                text: emojiCell.modelData.e
                font.family: root.fontFamily
                font.pixelSize: Style.font.display
              }
              MouseArea {
                anchors.fill: parent
                enabled: emojiCell.modelData.e !== ""
                hoverEnabled: true
                cursorShape: Qt.PointingHandCursor
                onContainsMouseChanged: if (containsMouse) root.pickerIndex = emojiCell.index
                onClicked: root.pickEmoji(emojiCell.modelData.e)
              }
            }

            Text {
              anchors.centerIn: parent
              width: parent.width - Style.space(20)
              visible: root.pickerItems.length === 0
              text: "No emoji matches “" + root.pickerQuery + "”"
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              horizontalAlignment: Text.AlignHCenter
              wrapMode: Text.WordWrap
            }
          }
        }
      }
    }

    // ---- dialogs: new chat, log out, keyboard shortcuts
    Item {
      id: dialogLayer
      anchors.fill: parent
      visible: root.dialog === "new" || root.dialog === "logout" || root.dialog === "help"
      z: 10

      Rectangle {
        anchors.fill: parent
        radius: card.radius
        color: Util.alpha(root.background, 0.82)
        MouseArea {
          anchors.fill: parent
          onClicked: if (!wa.loggingOut) root.closeDialog()
        }
      }

      BorderSurface {
        id: sheet
        anchors.centerIn: parent
        width: Math.min(parent.width - Style.space(40), root.dialog === "help" ? Style.space(640) : Style.space(480))
        height: Math.min(parent.height - Style.space(40), sheetBody.implicitHeight + sheet.contentTopInset + sheet.contentBottomInset)
        color: root.background
        borderSpec: Border.flat(root.dialog === "logout" ? Color.urgent : root.accent, Style.normalBorderWidth)
        padding: Style.space(18)
        radius: Style.cornerRadius

        MouseArea { anchors.fill: parent; onClicked: {} }

        Item {
          id: sheetBody
          anchors.fill: parent
          anchors.topMargin: sheet.contentTopInset
          anchors.rightMargin: sheet.contentRightInset
          anchors.bottomMargin: sheet.contentBottomInset
          anchors.leftMargin: sheet.contentLeftInset
          implicitHeight: root.dialog === "new" ? newChat.implicitHeight
            : (root.dialog === "logout" ? logoutSheet.implicitHeight : helpSheet.implicitHeight)

          // ---------- new chat
          ColumnLayout {
            id: newChat
            property string req: ""
            property string error: ""
            visible: root.dialog === "new"
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            spacing: Style.space(12)

            Text {
              text: Model.GLYPH_NEW_CHAT + "  New chat"
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.heading
              font.bold: true
            }
            Text {
              Layout.fillWidth: true
              text: "Type the phone number with country and area code. It is checked with WhatsApp before the chat opens."
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              wrapMode: Text.WordWrap
            }
            TextField {
              id: newChatField
              Layout.fillWidth: true
              placeholderText: "+55 11 98765-4321"
              foreground: root.foreground
              font.family: root.fontFamily
              inputMethodHints: Qt.ImhDialableCharactersOnly
              onTextChanged: newChat.error = ""
              Keys.onPressed: function(event) {
                if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) { root.checkNewChat(); event.accepted = true }
                else if (event.key === Qt.Key_Escape) { root.closeDialog(); event.accepted = true }
              }
            }
            Text {
              Layout.fillWidth: true
              visible: text !== ""
              text: newChat.req !== "" ? "Checking…"
                : (newChat.error !== "" ? Model.GLYPH_WARNING + " " + newChat.error
                   : (wa.online ? "" : "OmaWhats is off. Turn it on to start a new chat."))
              color: newChat.error !== "" ? Color.urgent : root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              wrapMode: Text.WordWrap
            }
            RowLayout {
              Layout.fillWidth: true
              spacing: Style.space(8)
              Item { Layout.fillWidth: true }
              Button {
                visible: !wa.live
                iconText: Model.GLYPH_POWER
                text: wa.starting ? "Turning on…" : "Turn on"
                bordered: true
                foreground: root.foreground
                fontFamily: root.fontFamily
                onClicked: wa.start()
              }
              Button {
                text: "Cancel"
                bordered: true
                foreground: root.foreground
                fontFamily: root.fontFamily
                onClicked: root.closeDialog()
              }
              Button {
                text: "Start chat"
                bordered: true
                selected: true
                foreground: root.foreground
                fontFamily: root.fontFamily
                onClicked: root.checkNewChat()
              }
            }
          }

          // ---------- log out
          ColumnLayout {
            id: logoutSheet
            property bool wipe: false
            property int choice: 0   // 0 cancel, 1 log out, 2 the delete toggle
            property string error: ""
            visible: root.dialog === "logout"
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            spacing: Style.space(12)

            function activate(i) {
              if (i === 0) { if (!wa.loggingOut) root.closeDialog() }
              else if (i === 1) { error = ""; wa.logout(wipe) }
              else wipe = !wipe
            }

            Keys.onPressed: function(event) {
              var k = event.key
              if (k === Qt.Key_Escape) logoutSheet.activate(0)
              else if (k === Qt.Key_Tab || k === Qt.Key_Right || k === Qt.Key_Down) logoutSheet.choice = (logoutSheet.choice + 1) % 3
              else if (k === Qt.Key_Backtab || k === Qt.Key_Left || k === Qt.Key_Up) logoutSheet.choice = (logoutSheet.choice + 2) % 3
              else if (k === Qt.Key_Space || k === Qt.Key_D) logoutSheet.wipe = !logoutSheet.wipe
              else if (k === Qt.Key_Return || k === Qt.Key_Enter) logoutSheet.activate(logoutSheet.choice)
              else return
              event.accepted = true
            }

            Text {
              text: Model.GLYPH_LOGOUT + "  Log out of WhatsApp?"
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.heading
              font.bold: true
            }
            Text {
              Layout.fillWidth: true
              text: "This unlinks this computer — “Omarchy” under Linked devices on your phone. "
                + "WhatsApp stops working here until you scan a new QR code."
                + (wa.live ? "" : " OmaWhats is off, so it will connect for a moment to log out.")
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              wrapMode: Text.WordWrap
            }
            Toggle {
              Layout.fillWidth: true
              label: "Also delete what is stored on this computer"
              description: "Chats, messages, downloaded media and pins. Left off, they stay readable here."
              checked: logoutSheet.wipe
              hasCursor: logoutSheet.choice === 2
              foreground: root.foreground
              accent: Color.urgent
              fontFamily: root.fontFamily
              onClicked: { logoutSheet.choice = 2; logoutSheet.wipe = !logoutSheet.wipe }
            }
            Text {
              Layout.fillWidth: true
              visible: text !== ""
              text: wa.loggingOut ? "Logging out…" : (logoutSheet.error !== "" ? Model.GLYPH_WARNING + " " + logoutSheet.error : "")
              color: logoutSheet.error !== "" && !wa.loggingOut ? Color.urgent : root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              wrapMode: Text.WordWrap
            }
            RowLayout {
              Layout.fillWidth: true
              spacing: Style.space(8)
              Item { Layout.fillWidth: true }
              Button {
                text: logoutSheet.error !== "" ? "Close" : "Cancel"
                bordered: true
                hasCursor: logoutSheet.choice === 0
                enabled: !wa.loggingOut
                foreground: root.foreground
                fontFamily: root.fontFamily
                onClicked: logoutSheet.activate(0)
              }
              Button {
                iconText: Model.GLYPH_LOGOUT
                text: logoutSheet.wipe ? "Log out and delete" : "Log out"
                bordered: true
                hasCursor: logoutSheet.choice === 1
                enabled: !wa.loggingOut
                foreground: Color.urgent
                accent: Color.urgent
                fontFamily: root.fontFamily
                onClicked: logoutSheet.activate(1)
              }
            }
          }

          // ---------- keyboard shortcuts
          ColumnLayout {
            id: helpSheet
            visible: root.dialog === "help"
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            spacing: Style.space(10)

            Keys.onPressed: function(event) {
              var k = event.key
              if (k === Qt.Key_Escape || k === Qt.Key_F1 || k === Qt.Key_Return || k === Qt.Key_Enter || k === Qt.Key_Q) root.closeDialog()
              else if (k === Qt.Key_Down) helpFlick.contentY = Math.min(Math.max(0, helpFlick.contentHeight - helpFlick.height), helpFlick.contentY + Style.space(40))
              else if (k === Qt.Key_Up) helpFlick.contentY = Math.max(0, helpFlick.contentY - Style.space(40))
              else return
              event.accepted = true
            }

            Text {
              text: Model.GLYPH_KEYBOARD + "  Keyboard shortcuts"
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.heading
              font.bold: true
            }

            Flickable {
              id: helpFlick
              Layout.fillWidth: true
              Layout.preferredHeight: Math.min(helpColumn.implicitHeight, dialogLayer.height - Style.space(200))
              contentWidth: width
              contentHeight: helpColumn.implicitHeight
              clip: true
              boundsBehavior: Flickable.StopAtBounds
              ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

              Column {
                id: helpColumn
                width: helpFlick.width - Style.space(12)
                spacing: Style.space(4)

                Repeater {
                  model: Model.shortcutRows()
                  RowLayout {
                    required property var modelData
                    width: helpColumn.width
                    spacing: Style.space(12)
                    Text {
                      Layout.fillWidth: true
                      Layout.topMargin: modelData.header ? Style.space(8) : 0
                      text: modelData.header ? String(modelData.text).toUpperCase() : modelData.text
                      color: modelData.header ? root.accent : root.foreground
                      font.family: root.fontFamily
                      font.pixelSize: modelData.header ? Style.font.caption : Style.font.bodySmall
                      font.bold: modelData.header
                      wrapMode: Text.WordWrap
                    }
                    Text {
                      visible: !modelData.header
                      text: modelData.keys
                      color: root.accent
                      font.family: root.fontFamily
                      font.pixelSize: Style.font.bodySmall
                      font.bold: true
                    }
                  }
                }
              }
            }

            Text {
              Layout.fillWidth: true
              text: "Mouse: right-click a chat to pin or unpin it. Bar icon: click for the panel, right-click for this window, middle-click to turn OmaWhats on or off."
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
              wrapMode: Text.WordWrap
            }
          }
        }
      }
    }

    // ---- image viewer
    Item {
      id: viewer
      anchors.fill: parent
      visible: root.dialog === "image"
      z: 12

      // 1 = fitted to the window; up to 8 times that.
      property real zoom: 1
      // The picture's own size, taken from the loaded picture — but read, not
      // bound to sourceSize: Qt says that size changed only by comparing it
      // with what the Image held before the new source began loading, so a
      // picture the same size as the one before it (a phone's photos all
      // share one size) never announces itself, the binding keeps the zero it
      // took while loading, and the picture is laid out 0 x 0 — on screen,
      // nothing at all. See readNatural, called whenever the Image moves.
      property real loadedW: 0
      property real loadedH: 0
      function readNatural() {
        var img = root.viewerSrc.animated ? animImg : stillImg
        if (img.status !== Image.Ready) return
        loadedW = img.sourceSize.width
        loadedH = img.sourceSize.height
      }
      // Until the picture is here, the size the attachment claims stands in
      // for it: a thumbnail is shown at the real photo's size, so the preview
      // is not postage-stamp, and a photo on its way is laid out where it
      // will land instead of flashing to nothing first.
      readonly property bool useMeta: !!root.viewerMedia && root.viewerMedia.width > 0 && root.viewerMedia.height > 0
        && (!root.viewerSrc.full || loadedW <= 0 || loadedH <= 0)
      readonly property real naturalW: useMeta ? root.viewerMedia.width : loadedW
      readonly property real naturalH: useMeta ? root.viewerMedia.height : loadedH
      readonly property real fit: Model.fitScale(naturalW, naturalH, viewFlick.width, viewFlick.height)
      readonly property real shownW: naturalW * fit * zoom
      readonly property real shownH: naturalH * fit * zoom
      // What the row says about the file: "loading" while it is on its way,
      // "" when all is well, anything else is what went wrong.
      readonly property string mediaState: {
        var r = root.viewerMediaJson !== "" ? root.viewerRow() : null
        return r ? r.mediaState : ""
      }
      readonly property bool loading: mediaState === "loading"
      readonly property string failure: loading || mediaState === "sending" ? "" : mediaState
      readonly property bool empty: root.viewerSrc.source === ""
      // A picture that is here and still shows nothing: the file did not open,
      // or it opened with no size to lay out. Without this the viewer just
      // goes black and says nothing, which is no answer to anyone.
      readonly property int imgStatus: root.viewerSrc.animated ? animImg.status : stillImg.status
      // Nothing on screen: nothing to show, a file that would not open, or one
      // that opened with no size to lay out.
      readonly property bool nothingShown: empty || imgStatus === Image.Error || naturalW <= 0 || naturalH <= 0
      readonly property bool broken: !empty && !loading && nothingShown &&
        imgStatus !== Image.Loading && imgStatus !== Image.Null
      onBrokenChanged: if (broken) {
        wa.trace("viewer " + root.viewerId + " shows nothing: status=" + imgStatus +
                 " size=" + Math.round(naturalW) + "x" + Math.round(naturalH) +
                 " full=" + root.viewerSrc.full)
      }
      // One line under the picture, or in place of it: what went wrong, or
      // why there is nothing to look at yet.
      readonly property string message: {
        if (failure !== "") return failure
        if (broken) return "This picture did not open" +
          (wa.live ? " — its copy here can be fetched again" : "")
        // While it is on its way, only where the wait is all there is to see:
        // over a picture the line at the top already says it.
        if (loading) return nothingShown ? "Downloading…" : ""
        if (!empty) return ""
        return wa.live ? "This picture has not been downloaded yet"
                       : "Turn OmaWhats on to see this picture"
      }
      // Worth offering another go: it failed, it shows nothing, or the full
      // picture is not here and nothing is fetching it.
      readonly property bool canRetry: failure !== "" || broken || (!root.viewerSrc.full && !loading)

      // Zoom about a point of the view (cx, cy), keeping it under the cursor.
      function zoomTo(z, cx, cy) {
        z = Math.max(1, Math.min(8, z))
        if (cx === undefined) { cx = viewFlick.width / 2; cy = viewFlick.height / 2 }
        var old = zoom
        var px = viewFlick.contentX + cx
        var py = viewFlick.contentY + cy
        zoom = z
        viewFlick.returnToBounds()
        viewFlick.contentX = Math.max(0, Math.min(viewFlick.contentWidth - viewFlick.width, px * z / old - cx))
        viewFlick.contentY = Math.max(0, Math.min(viewFlick.contentHeight - viewFlick.height, py * z / old - cy))
      }
      function actualSize() { if (fit > 0) zoomTo(1 / fit) }

      Rectangle {
        anchors.fill: parent
        radius: card.radius
        color: Qt.rgba(0, 0, 0, 0.95)
      }

      Item {
        id: viewerKeys
        anchors.fill: parent
        focus: true
        Keys.onPressed: function(event) {
          var k = event.key
          if (k === Qt.Key_Escape || k === Qt.Key_Q) root.closeDialog()
          else if (k === Qt.Key_Left || k === Qt.Key_PageUp) root.viewerStep(-1)
          else if (k === Qt.Key_Right || k === Qt.Key_PageDown || k === Qt.Key_Space) root.viewerStep(1)
          else if (k === Qt.Key_Home) root.viewerStep(-root.viewerPos)
          else if (k === Qt.Key_End) root.viewerStep(root.viewerIds.length - 1 - root.viewerPos)
          else if (k === Qt.Key_Plus || k === Qt.Key_Equal) viewer.zoomTo(viewer.zoom * 1.25)
          else if (k === Qt.Key_Minus || k === Qt.Key_Underscore) viewer.zoomTo(viewer.zoom / 1.25)
          else if (k === Qt.Key_0) viewer.zoomTo(1)
          else if (k === Qt.Key_1) viewer.actualSize()
          else if (k === Qt.Key_R) {
            if (!viewer.loading) root.retryMedia(root.viewerId)
          }
          else if (k === Qt.Key_O) {
            if (root.viewerSrc.full && root.viewerMedia && root.viewerMedia.file) root.openFile(root.viewerMedia.file)
          }
          else return
          event.accepted = true
        }
      }

      Flickable {
        id: viewFlick
        anchors.fill: parent
        anchors.topMargin: Style.space(56)
        anchors.bottomMargin: Style.space(64)
        anchors.leftMargin: Style.space(56)
        anchors.rightMargin: Style.space(56)
        clip: true
        contentWidth: Math.max(width, viewer.shownW)
        contentHeight: Math.max(height, viewer.shownH)
        boundsBehavior: Flickable.StopAtBounds
        interactive: viewer.zoom > 1

        Item {
          width: viewFlick.contentWidth
          height: viewFlick.contentHeight

          // Double-click: fitted ↔ actual size (2× for small pictures).
          TapHandler {
            onDoubleTapped: function(eventPoint) {
              if (viewer.zoom > 1.01) viewer.zoomTo(1)
              else viewer.zoomTo(Math.max(2, 1 / viewer.fit),
                eventPoint.position.x - viewFlick.contentX, eventPoint.position.y - viewFlick.contentY)
            }
          }

          Image {
            id: stillImg
            visible: !root.viewerSrc.animated
            anchors.centerIn: parent
            width: viewer.shownW
            height: viewer.shownH
            source: root.viewerSrc.animated ? "" : root.viewerSrc.source
            asynchronous: true
            cache: true
            smooth: true
            mipmap: true
            fillMode: Image.PreserveAspectFit
            // The size is forgotten the moment another picture is asked for,
            // and read back once that one is here. callLater, because a
            // picture already in the cache is ready before this returns.
            onSourceChanged: { viewer.loadedW = 0; viewer.loadedH = 0; Qt.callLater(viewer.readNatural) }
            onStatusChanged: viewer.readNatural()
            onSourceSizeChanged: viewer.readNatural()
          }

          AnimatedImage {
            id: animImg
            visible: root.viewerSrc.animated
            anchors.centerIn: parent
            width: viewer.shownW
            height: viewer.shownH
            source: root.viewerSrc.animated ? root.viewerSrc.source : ""
            playing: viewer.visible
            asynchronous: true
            cache: false
            smooth: true
            fillMode: Image.PreserveAspectFit
            onSourceChanged: { viewer.loadedW = 0; viewer.loadedH = 0; Qt.callLater(viewer.readNatural) }
            onStatusChanged: viewer.readNatural()
            onSourceSizeChanged: viewer.readNatural()
          }
        }

        // Wheel zooms about the cursor; only the wheel, so dragging still
        // pans the Flickable.
        MouseArea {
          anchors.fill: parent
          acceptedButtons: Qt.NoButton
          onWheel: function(wheel) {
            var f = wheel.angleDelta.y > 0 ? 1.2 : 1 / 1.2
            viewer.zoomTo(viewer.zoom * f, wheel.x - viewFlick.contentX, wheel.y - viewFlick.contentY)
          }
        }
      }

      // What is going on with this picture, and a way to ask for it again.
      // Over the middle while there is nothing to look at; down at the foot
      // of the window once a preview stands in, so it is not in the way.
      Row {
        id: viewerState
        spacing: Style.space(10)
        visible: viewer.message !== "" || viewer.canRetry
        anchors.horizontalCenter: viewFlick.horizontalCenter
        // Placed by hand rather than re-anchored: an anchor taken away again
        // does not give the item its old place back.
        y: viewer.nothingShown ? viewFlick.y + (viewFlick.height - height) / 2
                               : viewFlick.y + viewFlick.height - height - Style.space(10)

        Text {
          anchors.verticalCenter: parent.verticalCenter
          visible: viewer.failure !== "" || viewer.broken
          text: Model.GLYPH_WARNING
          color: Color.urgent
          font.family: root.fontFamily
          font.pixelSize: Style.font.body
        }
        Text {
          anchors.verticalCenter: parent.verticalCenter
          visible: viewer.message !== ""
          width: Math.max(0, Math.min(implicitWidth, viewFlick.width - Style.space(180)))
          text: viewer.message
          color: "white"
          opacity: 0.8
          font.family: root.fontFamily
          font.pixelSize: Style.font.body
          elide: Text.ElideRight
        }
        ViewerButton {
          anchors.verticalCenter: parent.verticalCenter
          visible: viewer.canRetry
          enabled: wa.live
          label: viewer.failure !== "" || viewer.broken ? "Try again" : "Download"
          tip: wa.live ? "Download this picture again (R)" : "Turn OmaWhats on to download"
          onClicked: root.retryMedia(root.viewerId)
        }
      }

      // ---- top bar
      RowLayout {
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: parent.top
        anchors.margins: Style.space(12)
        spacing: Style.space(8)

        ColumnLayout {
          Layout.fillWidth: true
          spacing: 0
          Text {
            Layout.fillWidth: true
            text: {
              var r = root.viewerMediaJson !== "" ? root.viewerRow() : null
              return r ? (r.fromMe ? "You" : (r.senderName || root.currentTitle)) : ""
            }
            color: "white"
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            font.bold: true
            elide: Text.ElideRight
          }
          Text {
            Layout.fillWidth: true
            text: {
              var r = root.viewerMediaJson !== "" ? root.viewerRow() : null
              var parts = []
              if (root.viewerIds.length > 1) parts.push((root.viewerPos + 1) + " of " + root.viewerIds.length)
              if (r) parts.push(Model.dayLabel(r.ts, root.nowMs) + " " + Model.clockTime(r.ts))
              if (viewer.loading) parts.push("downloading the full picture…")
              else if (!root.viewerSrc.full && root.viewerSrc.source !== "") parts.push("preview only" + (wa.live ? "" : " — turn OmaWhats on for the full picture"))
              if (root.viewerSrc.full && (viewer.zoom > 1.01 || viewer.fit < 0.999)) parts.push(Math.round(viewer.fit * viewer.zoom * 100) + "%")
              return parts.join(" · ")
            }
            color: Qt.rgba(1, 1, 1, 0.65)
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            elide: Text.ElideRight
          }
        }

        ViewerButton { label: "−"; big: true; tip: "Zoom out (−)"; onClicked: viewer.zoomTo(viewer.zoom / 1.25) }
        ViewerButton { label: "+"; big: true; tip: "Zoom in (+)"; onClicked: viewer.zoomTo(viewer.zoom * 1.25) }
        ViewerButton { label: viewer.zoom > 1.01 ? "Fit" : "100%"; tip: "Fit to window (0) / actual size (1)"; onClicked: viewer.zoom > 1.01 ? viewer.zoomTo(1) : viewer.actualSize() }
        // Always there, whatever state the picture is in: a copy that opens
        // can still be the wrong one, or half of one, and only the person
        // looking at it can tell.
        ViewerButton {
          glyph: Model.GLYPH_REFRESH
          // Kept live with OmaWhats off as well: dimmed on black it reads as
          // absent, and the answer to a click then says what to do about it.
          enabled: !viewer.loading
          tip: !wa.live ? "Turn OmaWhats on to download"
             : viewer.loading ? "Downloading…"
             : Model.hasCachedFile(root.viewerMedia) ? "Fetch this picture again (R)"
             : "Download this picture (R)"
          onClicked: root.retryMedia(root.viewerId)
        }
        ViewerButton {
          glyph: Model.GLYPH_OPEN_EXTERNAL
          tip: "Open in the system image viewer (O)"
          enabled: root.viewerSrc.full && !!root.viewerMedia && !!root.viewerMedia.file
          onClicked: root.openFile(root.viewerMedia.file)
        }
        ViewerButton { glyph: Model.GLYPH_CLOSE; tip: "Close (Esc)"; onClicked: root.closeDialog() }
      }

      // ---- previous / next
      ViewerButton {
        anchors.left: parent.left
        anchors.leftMargin: Style.space(10)
        anchors.verticalCenter: viewFlick.verticalCenter
        visible: root.viewerPos > 0
        glyph: Model.GLYPH_LEFT
        tip: "Previous (←)"
        onClicked: root.viewerStep(-1)
      }
      ViewerButton {
        anchors.right: parent.right
        anchors.rightMargin: Style.space(10)
        anchors.verticalCenter: viewFlick.verticalCenter
        visible: root.viewerPos >= 0 && root.viewerPos < root.viewerIds.length - 1
        glyph: Model.GLYPH_RIGHT
        tip: "Next (→)"
        onClicked: root.viewerStep(1)
      }

      // ---- caption
      Text {
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.bottom: parent.bottom
        anchors.margins: Style.space(14)
        visible: text !== ""
        text: root.viewerMedia && root.viewerMedia.caption ? String(root.viewerMedia.caption) : ""
        color: "white"
        font.family: root.fontFamily
        font.pixelSize: Style.font.body
        horizontalAlignment: Text.AlignHCenter
        wrapMode: Text.WordWrap
        maximumLineCount: 3
        elide: Text.ElideRight
      }
    }
  }

  // A small round button beside a message (react, reply).
  component MessageAction: Rectangle {
    id: ma
    property string glyph: ""
    property string tip: ""
    signal clicked()
    width: Style.space(28)
    height: width
    radius: width / 2
    color: maMouse.containsMouse ? Util.alpha(root.foreground, 0.18) : Util.alpha(root.foreground, 0.08)
    Text {
      anchors.centerIn: parent
      text: ma.glyph
      color: root.foreground
      font.family: root.fontFamily
      font.pixelSize: Style.font.bodySmall
    }
    MouseArea {
      id: maMouse
      anchors.fill: parent
      hoverEnabled: true
      cursorShape: Qt.PointingHandCursor
      onClicked: ma.clicked()
    }
    ToolTip.visible: ma.tip !== "" && maMouse.containsMouse
    ToolTip.text: ma.tip
    ToolTip.delay: 400
  }

  // A round, white-on-dark button for the image viewer.
  component ViewerButton: Rectangle {
    id: vb
    property string glyph: ""
    property string label: ""
    property string tip: ""
    property bool big: false
    signal clicked()
    implicitWidth: Math.max(Style.space(36), vbText.implicitWidth + Style.space(18))
    implicitHeight: Style.space(36)
    width: implicitWidth
    height: implicitHeight
    radius: height / 2
    opacity: enabled ? 1.0 : 0.35
    color: vbMouse.containsMouse && enabled ? Qt.rgba(1, 1, 1, 0.22) : Qt.rgba(1, 1, 1, 0.1)
    Text {
      id: vbText
      anchors.centerIn: parent
      text: vb.glyph !== "" ? vb.glyph : vb.label
      color: "white"
      font.family: root.fontFamily
      font.pixelSize: vb.glyph !== "" || vb.big ? Style.font.icon : Style.font.caption
      font.bold: vb.glyph === ""
    }
    MouseArea {
      id: vbMouse
      anchors.fill: parent
      hoverEnabled: true
      cursorShape: vb.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
      onClicked: if (vb.enabled) vb.clicked()
    }
    ToolTip.visible: vb.tip !== "" && vbMouse.containsMouse
    ToolTip.text: vb.tip
    ToolTip.delay: 400
  }
}
