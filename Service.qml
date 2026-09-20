import QtQuick
import Quickshell
import Quickshell.Io
import Qt.labs.folderlistmodel
import "Model.js" as Model

// The plugin's one link to omawhatsd.
//
// The daemon is not a service: it runs only between a start and a stop. While
// it runs, this talks to it over its unix socket (JSON lines) and everything is
// live. While it is stopped, the same two lines a live hello would return are
// read from `omawhatsd snapshot`, so old conversations stay readable
// and the bar badge keeps its last count.
//
// Both the bar panel and the full-screen client create one of these; the
// daemon broadcasts to every connection, so they never disagree for long.
Item {
  id: root

  property var settings: ({})

  property bool live: false
  // Not `state`/`focus`: this is an Item, and both are Item properties.
  property var daemonState: Model.defaultState()
  // As the daemon listed them (newest first) and as shown (pins first).
  property var rawChats: []
  readonly property var chats: Model.applyPins(rawChats, pins)
  // Pinned chat JIDs in order; see pinsFile below.
  property var pins: []
  property bool starting: false
  property bool stopping: false
  property string startError: ""
  // The chat this instance is showing, re-announced after every reconnect so
  // the daemon keeps treating it as read.
  property string focusChat: ""

  readonly property int unread: Model.totalUnread(chats)
  readonly property bool online: Model.isOnline(daemonState, live)
  readonly property bool canSend: online

  // info: {more, asked, end, offline} — see the "history" reply in server.go.
  signal historyReceived(string chat, var messages, double before, var info)
  signal olderReceived(string chat, int count, bool end, bool timedOut)
  signal checkResult(string req, bool ok, string jid, string name, string error)
  signal logoutFinished(bool ok, string error)
  signal messageReceived(string chat, var message, bool update, string req)
  signal statusReceived(string chat, string id, string status)
  signal sendResult(string req, string chat, bool ok, string error, string id)
  signal reactResult(string req, string chat, string id, bool ok, string error)
  signal mediaResult(string req, string chat, string id, bool ok, string error, var media, bool open)
  signal becameLive()
  signal daemonError(string error)

  readonly property string home: Quickshell.env("HOME")
  readonly property string binary: {
    var b = settings && settings.binary ? String(settings.binary) : ""
    return b !== "" ? b.replace(/^~/, home) : home + "/.local/bin/omawhatsd"
  }
  readonly property string dataDir: Model.dataDir({
    override: Quickshell.env("OMAWHATS_DATA"),
    dataHome: Quickshell.env("XDG_DATA_HOME"),
    home: home
  })
  readonly property string cacheDir: Model.cacheDir({
    override: Quickshell.env("OMAWHATS_CACHE"),
    cacheHome: Quickshell.env("XDG_CACHE_HOME"),
    home: home
  })
  readonly property string socketPath: Model.socketPath({
    override: Quickshell.env("OMAWHATS_SOCKET"),
    runtimeDir: Quickshell.env("XDG_RUNTIME_DIR"),
    dataHome: Quickshell.env("XDG_DATA_HOME"),
    home: home
  })

  // The last few connection events, readable with
  // `omarchy-shell megamvb.omawhats debug` — plugin console output does not
  // reach the shell log.
  property string debugTrace: ""
  function trace(line) {
    var stamp = new Date().toTimeString().substring(0, 8)
    debugTrace = (debugTrace + "\n" + stamp + " " + line).split("\n").slice(-30).join("\n").replace(/^\n/, "")
  }

  property int _reqSeq: 0
  property var _pendingHistory: null

  function write(obj) {
    if (!sock || !sock.connected) return false
    sock.write(JSON.stringify(obj) + "\n")
    sock.flush()
    return true
  }

  function handleLine(line) {
    var obj = Model.parseLine(line)
    if (!obj) return
    switch (obj.type) {
    case "state":
      root.daemonState = Model.normalizeState(obj)
      break
    case "chats":
      root.rawChats = Array.isArray(obj.chats) ? obj.chats : []
      break
    case "history":
      root.historyReceived(String(obj.chat || ""), obj.messages || [], Number(obj.before) || 0, root.historyInfo(obj))
      break
    case "older":
      root.olderReceived(String(obj.chat || ""), Number(obj.count) || 0, obj.end === true, obj.timeout === true)
      break
    case "check":
      root.checkResult(String(obj.req || ""), obj.ok === true, String(obj.jid || ""), String(obj.name || ""), String(obj.error || ""))
      break
    case "loggedout":
      root.finishLogout(obj.ok === true, String(obj.error || ""))
      break
    case "message":
      root.messageReceived(String(obj.chat || ""), obj.message, obj.update === true, String(obj.req || ""))
      break
    case "status":
      root.statusReceived(String(obj.chat || ""), String(obj.id || ""), String(obj.status || ""))
      break
    case "sent":
      root.sendResult(String(obj.req || ""), String(obj.chat || ""), obj.ok === true, String(obj.error || ""), String(obj.id || ""))
      break
    case "reacted":
      root.reactResult(String(obj.req || ""), String(obj.chat || ""), String(obj.id || ""), obj.ok === true, String(obj.error || ""))
      break
    case "media":
      root.mediaResult(String(obj.req || ""), String(obj.chat || ""), String(obj.id || ""), obj.ok === true,
        String(obj.error || ""), obj.media || null, obj.open === true)
      break
    case "error":
      root.daemonError(String(obj.error || ""))
      break
    case "bye":
      root.stopping = true
      break
    }
  }

  function historyInfo(obj) {
    return { more: obj.more === true, asked: obj.asked === true, end: obj.end === true, offline: obj.offline === true }
  }

  // ---- control

  function start() {
    if (live || startProcess.running) return
    starting = true
    startError = ""
    _attempts = 0
    startProcess.command = [binary].concat(Model.startArgs(settings))
    startProcess.running = true
  }

  function stop() {
    if (!live) return
    stopping = true
    write({ cmd: "quit" })
  }

  function togglePower() {
    if (live) stop()
    else start()
  }

  function pair() { write({ cmd: "pair" }) }

  // Unlinks this computer from the account (wipe: and deletes everything
  // stored locally). Works with the daemon off too: the CLI then connects
  // just long enough to log out. The answer comes back as logoutFinished.
  property bool loggingOut: false
  function logout(wipe) {
    if (loggingOut) return
    loggingOut = true
    if (live && write({ cmd: "logout", wipe: wipe === true, req: "logout" })) return
    logoutProcess.command = [binary, "logout"].concat(wipe === true ? ["--wipe"] : [])
    logoutProcess.running = true
  }

  function finishLogout(ok, error) {
    loggingOut = false
    refresh()
    logoutFinished(ok, error)
  }

  // Resolves a typed phone number to the chat WhatsApp knows it by; the
  // answer comes back as checkResult. Needs the daemon connected.
  function checkNumber(text) {
    if (!online) return ""
    _reqSeq += 1
    var req = "c" + Date.now() + "-" + _reqSeq
    return write({ cmd: "check", text: String(text || ""), req: req }) ? req : ""
  }

  // ---- pins

  function setPins(list) {
    pins = list
    pinsFile.setText(JSON.stringify(list) + "\n")
    // The daemon lists pinned chats however old they are.
    refresh()
  }
  function togglePin(jid) { setPins(Model.togglePin(pins, jid)) }
  function movePin(jid, delta) { setPins(Model.movePin(pins, jid, delta)) }

  FileView {
    id: pinsFile
    path: root.dataDir + "/pins.json"
    watchChanges: true
    atomicWrites: true
    printErrors: false
    onFileChanged: reload()
    onLoaded: root.pins = Model.parsePins(text())
    onLoadFailed: root.pins = []
  }

  // Returns the request id the matching sendResult will carry, or "" when the
  // message could not even be handed to the daemon. quote: the id of the
  // message this one answers, if any.
  function send(chat, text, quote) {
    if (!canSend) return ""
    _reqSeq += 1
    var req = "r" + Date.now() + "-" + _reqSeq
    return write({ cmd: "send", chat: chat, text: text, quote: quote || "", req: req }) ? req : ""
  }

  // Sends a file on this computer (path is absolute); caption and quote go
  // with it. Files are sent one at a time, in order; the answer is a
  // sendResult like a text's.
  function sendFile(chat, path, caption, quote, asDocument) {
    if (!canSend) return ""
    _reqSeq += 1
    var req = "f" + Date.now() + "-" + _reqSeq
    return write({ cmd: "sendfile", chat: chat, path: path, text: caption || "", quote: quote || "",
      asDocument: asDocument === true, req: req }) ? req : ""
  }

  // Sets this account's reaction to a message ("" takes it back). The answer
  // comes back as reactResult; on success every client also gets the message
  // re-sent with its reactions.
  function react(chat, id, emoji) {
    if (!canSend) return ""
    _reqSeq += 1
    var req = "e" + Date.now() + "-" + _reqSeq
    return write({ cmd: "react", chat: chat, id: id, emoji: emoji || "", req: req }) ? req : ""
  }

  // Asks the daemon to download a message's attachment; the answer comes back
  // as mediaResult, and every client also gets the message re-sent with the
  // file filled in. force says the copy already here is no good: the daemon
  // throws it away and fetches another, under a name of its own.
  function requestMedia(chat, id, open, force) {
    if (!live) return ""
    _reqSeq += 1
    var req = "m" + Date.now() + "-" + _reqSeq
    return write({ cmd: "media", chat: chat, id: id, open: open === true,
                   force: force === true, req: req }) ? req : ""
  }

  function focusOn(chat) {
    focusChat = chat || ""
    write({ cmd: "focus", chat: focusChat })
  }

  // Messages older than the cursor (the oldest the client holds); no cursor
  // for the newest page. Live, the daemon pages and, past the local copy, asks
  // the phone. Offline, the stored copy is paged through the CLI.
  function requestHistory(chat, before, beforeId, noAsk) {
    trace("requestHistory " + (live ? "live" : "offline") + " before=" + (before || 0))
    if (!chat) return
    if (live) {
      write({ cmd: "history", chat: chat, limit: 80, before: before || 0, beforeId: beforeId || "", noAsk: noAsk === true })
      return
    }
    _pendingHistory = { chat: chat, before: before || 0, beforeId: beforeId || "" }
    if (historyProcess.running) return
    runHistory()
  }

  function runHistory() {
    var p = _pendingHistory
    _pendingHistory = null
    historyProcess.command = [binary, "history", p.chat, "80", String(p.before), p.beforeId]
    historyProcess.running = true
  }

  function refresh() {
    if (live) write({ cmd: "chats" })
    else loadSnapshot()
  }

  function loadSnapshot() {
    if (live || snapshotProcess.running) return
    snapshotProcess.running = true
  }

  // ---- socket

  // The daemon drops "<socket>.pid" next to its socket once it is listening
  // and removes it on the way out. Watching that directory is how a start —
  // from here, a terminal, or a keybinding — is noticed immediately, without
  // polling a socket that is usually not there.
  FolderListModel {
    id: runDir
    folder: Model.fileUrl(Model.dirName(root.socketPath))
    nameFilters: [Model.baseName(root.socketPath) + ".pid"]
    showDirs: false
    showHidden: true
    onCountChanged: {
      root.trace("daemon marker " + (count > 0 ? "appeared" : "gone"))
      if (count > 0 && !root.live) root.connectNow()
    }
  }

  property int _attempts: 0

  // A Socket does not retry on its own, and flipping `connected` off and on
  // in one tick is a no-op, so each attempt is a fresh Socket.
  property var sock: null

  function connectNow() {
    if (sock) {
      var old = sock
      sock = null
      old.destroy()
    }
    // Created disconnected and assigned before connecting: a local socket
    // can connect synchronously, and the handler only listens to root.sock.
    sock = socketComponent.createObject(root)
    sock.connected = true
  }

  Component.onDestruction: if (sock) sock.destroy()

  Component {
    id: socketComponent
    Socket {
      id: socketObj
      path: root.socketPath
      connected: false
      parser: SplitParser {
        onRead: function(data) { root.handleLine(data) }
      }
      // `connected` notifies through connectionStateChanged. A replaced
      // socket being torn down must not speak for its successor.
      onConnectionStateChanged: {
        if (socketObj !== root.sock) return
        root.trace(connected ? "socket connected" : "socket closed")
        if (connected) {
          root._attempts = 0
          root.live = true
          root.starting = false
          root.stopping = false
          root.write({ cmd: "hello" })
          if (root.focusChat !== "") root.write({ cmd: "focus", chat: root.focusChat })
          root.becameLive()
        } else if (root.live) {
          root.live = false
          root.stopping = false
          root.daemonState = Model.defaultState()
          root.loadSnapshot()
        }
      }
      onError: if (socketObj === root.sock) retry.restart()
    }
  }

  // Retries only while there is reason to expect a daemon: one is starting,
  // or its marker exists (a marker left by a crash gives up after a few).
  Timer {
    id: retry
    interval: root.starting ? 250 : 2000
    repeat: false
    onTriggered: {
      if (root.live) return
      if (root.starting || (runDir.count > 0 && root._attempts < 5)) {
        root._attempts += 1
        root.connectNow()
      }
    }
  }

  Process {
    id: startProcess
    running: false
    property string errText: ""
    stdout: StdioCollector { id: startOut; waitForEnd: true }
    stderr: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        startProcess.errText = String(text || "").trim()
        if (startProcess.errText !== "" && !root.live) root.startError = startProcess.errText.split("\n").pop()
      }
    }
    onStarted: errText = ""
    onExited: function(exitCode) {
      root.trace("start helper exited " + exitCode)
      if (exitCode === 0) {
        if (!root.live) root.connectNow()
      } else {
        root.starting = false
        if (root.startError === "")
          root.startError = errText !== "" ? errText.split("\n").pop() : "Could not start " + root.binary
      }
    }
  }

  // Covers a start that never produced a socket without the start helper
  // noticing (it gives up after 10 s itself).
  Timer {
    interval: 15000
    running: root.starting
    onTriggered: root.starting = false
  }

  // Output is handled in onStreamFinished, not onExited: the process can be
  // reported exited before its collector has the last of stdout.
  Process {
    id: snapshotProcess
    running: false
    command: [root.binary, "snapshot"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        if (root.live) return
        var lines = Model.parseLines(text)
        for (var i = 0; i < lines.length; i++) {
          if (lines[i].type === "state") root.daemonState = Model.normalizeState(lines[i])
          else if (lines[i].type === "chats") root.rawChats = lines[i].chats || []
        }
      }
    }
    onExited: function(exitCode) {
      if (exitCode !== 0) root.startError = "Daemon not found at " + root.binary + " — run install-daemon in the plugin folder."
    }
  }

  Process {
    id: historyProcess
    running: false
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        var lines = Model.parseLines(text)
        root.trace("history output lines=" + lines.length + " bytes=" + String(text).length)
        for (var i = 0; i < lines.length; i++) {
          if (lines[i].type === "history")
            root.historyReceived(String(lines[i].chat || ""), lines[i].messages || [], Number(lines[i].before) || 0, root.historyInfo(lines[i]))
        }
      }
    }
    onExited: if (root._pendingHistory) root.runHistory()
  }

  Process {
    id: logoutProcess
    running: false
    property string errText: ""
    stderr: StdioCollector {
      waitForEnd: true
      onStreamFinished: logoutProcess.errText = String(text || "").trim().replace(/^omawhatsd: /, "")
    }
    onExited: function(exitCode) {
      root.finishLogout(exitCode === 0, exitCode === 0 ? "" : (errText || "Logging out failed."))
    }
  }

  Component.onCompleted: {
    loadSnapshot()
    connectNow()
  }
}
