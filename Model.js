.pragma library

// The pure half of the OmaWhats plugin: parsing the daemon's lines, the words
// shown for each state, and time formatting. Nothing in here touches QML, so
// tests/run.js can load it under plain node.

var GLYPH_WHATSAPP = ""
var GLYPH_POWER = ""
var GLYPH_SEND = "\u{f048a}"
var GLYPH_CHECK = "\u{f012c}"
var GLYPH_CHECK_ALL = "\u{f012d}"
var GLYPH_GROUP = "\u{f0849}"
var GLYPH_PERSON = "\u{f0004}"
var GLYPH_QR = "\u{f0432}"
var GLYPH_LOGOUT = "\u{f0343}"
var GLYPH_CLOCK = "\u{f0150}"
var GLYPH_REFRESH = "\u{f0450}"
var GLYPH_SEARCH = "\u{f0349}"
var GLYPH_PIN = "\u{f0403}"
var GLYPH_UNPIN = "\u{f0404}"
var GLYPH_NEW_CHAT = "\u{f0653}"
var GLYPH_KEYBOARD = "\u{f030c}"
var GLYPH_CLOSE = "\u{f0156}"
var GLYPH_UP = "\u{f005d}"
var GLYPH_DOWN = "\u{f0045}"
var GLYPH_REPLY = "\u{f045a}"
var GLYPH_EMOJI = "\u{f01f5}"
var GLYPH_WINDOW = "\u{f05af}"
var GLYPH_POPUP = "\u{f05b2}"
var GLYPH_ATTACH = "\u{f03e2}"
var GLYPH_UPLOAD = "\u{f0552}"
var GLYPH_FILE_IMAGE = "\u{f021f}"

function defaultState() {
  return {
    type: "state", state: "stopped", running: false, paired: false,
    me: "", meName: "", qr: [], error: "", readReceipts: true, syncing: false
  }
}

function socketPath(env) {
  env = env || {}
  if (env.override) return String(env.override)
  if (env.runtimeDir) return String(env.runtimeDir) + "/omawhats.sock"
  var data = env.dataHome ? String(env.dataHome) : String(env.home || "") + "/.local/share"
  return data + "/omawhats/daemon.sock"
}

function dirName(path) {
  var s = String(path || "")
  var i = s.lastIndexOf("/")
  return i > 0 ? s.substring(0, i) : "/"
}

function baseName(path) {
  var s = String(path || "")
  return s.substring(s.lastIndexOf("/") + 1)
}

// One JSON object per line. Anything unparseable is dropped rather than
// allowed to throw inside a signal handler.
function parseLine(line) {
  var s = String(line || "").trim()
  if (s === "") return null
  try {
    var obj = JSON.parse(s)
    return obj && typeof obj === "object" && typeof obj.type === "string" ? obj : null
  } catch (e) {
    return null
  }
}

function parseLines(text) {
  var out = []
  var lines = String(text || "").split("\n")
  for (var i = 0; i < lines.length; i++) {
    var obj = parseLine(lines[i])
    if (obj) out.push(obj)
  }
  return out
}

function normalizeState(obj) {
  var s = defaultState()
  if (!obj) return s
  for (var k in s) if (obj[k] !== undefined && obj[k] !== null) s[k] = obj[k]
  if (!Array.isArray(s.qr)) s.qr = []
  return s
}

function totalUnread(chats) {
  var n = 0
  if (!Array.isArray(chats)) return 0
  for (var i = 0; i < chats.length; i++) n += Math.max(0, parseInt(chats[i].unread, 10) || 0)
  return n
}

function unreadBadge(n) {
  if (!n || n <= 0) return ""
  return n > 99 ? "99+" : String(n)
}

function formatPhone(me) {
  var d = String(me || "").replace(/\D/g, "")
  if (d === "") return ""
  // Brazilian numbers get their familiar shape; anything else stays +digits.
  var m = d.match(/^55(\d{2})(\d{4,5})(\d{4})$/)
  if (m) return "+55 " + m[1] + " " + m[2] + "-" + m[3]
  return "+" + d
}

// The headline for each daemon state. `live` is whether the plugin is talking
// to a running daemon at all.
function stateLabel(state, live) {
  var s = state ? state.state : "stopped"
  if (!live) return state && state.paired ? "Off" : "Off · not linked"
  switch (s) {
  case "starting": return "Starting…"
  case "connecting": return "Connecting…"
  case "pairing": return "Scan the QR code with your phone"
  case "pair-timeout": return "The QR code expired"
  case "connected": return state.syncing ? "Connected · syncing…" : "Connected"
  case "reconnecting": return "Reconnecting…"
  case "loggedout": return "Logged out"
  case "error": return "Error"
  }
  return s
}

function stateDetail(state, live) {
  if (!state) return ""
  var who = state.meName ? state.meName : ""
  var phone = formatPhone(state.me)
  if (who && phone) return who + " · " + phone
  return who || phone
}

// Whether the glyph in the bar should be dimmed: anything short of a working
// connection.
function isOnline(state, live) {
  return !!live && !!state && state.state === "connected"
}

function isAlarm(state, live) {
  return !!live && !!state && (state.state === "error" || state.state === "loggedout")
}

function needsPairing(state, live) {
  return !!live && !!state && (state.state === "pairing" || state.state === "pair-timeout" || state.state === "loggedout")
}

function pad2(n) { return (n < 10 ? "0" : "") + n }

var WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
var MONTHS_SHORT = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]

function sameDay(a, b) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

// Chat-list time: clock today, "Yesterday", weekday within the week, else date.
function listTime(ts, nowMs) {
  ts = Number(ts) || 0
  if (ts <= 0) return ""
  var d = new Date(ts * 1000)
  var now = new Date(nowMs || Date.now())
  if (sameDay(d, now)) return pad2(d.getHours()) + ":" + pad2(d.getMinutes())
  var y = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1)
  if (sameDay(d, y)) return "Yesterday"
  var diffDays = (new Date(now.getFullYear(), now.getMonth(), now.getDate()) - new Date(d.getFullYear(), d.getMonth(), d.getDate())) / 86400000
  if (diffDays < 7) return WEEKDAYS[d.getDay()]
  if (d.getFullYear() === now.getFullYear()) return MONTHS_SHORT[d.getMonth()] + " " + d.getDate()
  return MONTHS_SHORT[d.getMonth()] + " " + d.getDate() + ", " + d.getFullYear()
}

function clockTime(ts) {
  ts = Number(ts) || 0
  if (ts <= 0) return ""
  var d = new Date(ts * 1000)
  return pad2(d.getHours()) + ":" + pad2(d.getMinutes())
}

var MONTHS = ["January", "February", "March", "April", "May", "June", "July",
  "August", "September", "October", "November", "December"]

function dayLabel(ts, nowMs) {
  ts = Number(ts) || 0
  if (ts <= 0) return ""
  var d = new Date(ts * 1000)
  var now = new Date(nowMs || Date.now())
  if (sameDay(d, now)) return "Today"
  var y = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1)
  if (sameDay(d, y)) return "Yesterday"
  var s = MONTHS[d.getMonth()] + " " + d.getDate()
  if (d.getFullYear() !== now.getFullYear()) s += ", " + d.getFullYear()
  return s
}

function differentDay(tsA, tsB) {
  if (!tsA || !tsB) return true
  return !sameDay(new Date(tsA * 1000), new Date(tsB * 1000))
}

function chatName(chat) {
  if (!chat) return ""
  if (chat.name) return String(chat.name)
  var jid = String(chat.jid || "")
  var user = jid.split("@")[0]
  return jid.indexOf("@s.whatsapp.net") > 0 ? formatPhone(user) : user
}

function chatPreview(chat) {
  if (!chat) return ""
  // One line, whatever the message looked like.
  var text = String(chat.preview || "").replace(/\s+/g, " ").trim()
  if (chat.previewFromMe) return "You: " + text
  if (chat.group && chat.previewSender) return chat.previewSender + ": " + text
  return text
}

function statusGlyph(status) {
  switch (String(status || "")) {
  case "pending": return GLYPH_CLOCK
  case "sent": return GLYPH_CHECK
  case "delivered":
  case "read": return GLYPH_CHECK_ALL
  case "failed": return "!"
  }
  return ""
}

// Case- and accent-insensitive substring match over name and phone number.
function fold(s) {
  s = String(s || "").toLowerCase()
  return typeof s.normalize === "function" ? s.normalize("NFD").replace(/[̀-ͯ]/g, "") : s
}

function filterChats(chats, query) {
  if (!Array.isArray(chats)) return []
  var q = fold(query).trim()
  if (q === "") return chats
  var out = []
  for (var i = 0; i < chats.length; i++) {
    var c = chats[i]
    if (fold(chatName(c)).indexOf(q) !== -1 || String(c.jid || "").indexOf(q) !== -1) out.push(c)
  }
  return out
}

function findChat(chats, jid) {
  if (!Array.isArray(chats)) return null
  for (var i = 0; i < chats.length; i++) if (chats[i].jid === jid) return chats[i]
  return null
}

// Accepts a number typed in any common shape and returns a JID to start a new
// conversation with, or "" when it cannot be a phone number.
function jidFromPhone(input) {
  var d = String(input || "").replace(/\D/g, "")
  if (d.length < 10 || d.length > 15) return ""
  return d + "@s.whatsapp.net"
}

function startArgs(settings) {
  settings = settings || {}
  var args = ["start"]
  if (settings.notifications === false) args.push("--no-notify")
  if (settings.readReceipts === false) args.push("--no-read-receipts")
  return args
}

// Widget settings live inline in the bar layout entry of shell.json. The bar
// panel is handed them; the full-screen client is not, so it digs them out.
function widgetSettings(shellJsonText, id) {
  var cfg
  try { cfg = JSON.parse(String(shellJsonText || "{}")) } catch (e) { return {} }
  var layout = cfg && cfg.bar && cfg.bar.layout ? cfg.bar.layout : {}
  for (var section in layout) {
    var entries = layout[section]
    if (!Array.isArray(entries)) continue
    for (var i = 0; i < entries.length; i++) {
      if (entries[i] && entries[i].id === id) return entries[i]
    }
  }
  return {}
}

// ---- media and rich text

var GLYPH_PLAY = "\u{f040a}"
var GLYPH_DOCUMENT = "\u{f0219}"
var GLYPH_MIC = "\u{f036c}"
var GLYPH_MUSIC = "\u{f075a}"
var GLYPH_LINK = "\u{f0337}"
var GLYPH_DOWNLOAD = "\u{f01da}"
var GLYPH_WARNING = "\u{f0026}"
var GLYPH_PAUSE = "\u{f03e4}"
var GLYPH_OPEN_EXTERNAL = "\u{f03cc}"
var GLYPH_LEFT = "\u{f004d}"
var GLYPH_RIGHT = "\u{f0054}"

function parseJson(s) {
  if (!s) return null
  try { return JSON.parse(String(s)) } catch (e) { return null }
}

function escapeHtml(s) {
  return String(s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;")
}

// Trailing punctuation is left out of a link, so "see https://x.com." links
// to x.com and not "x.com.".
var URL_RE = /(\bhttps?:\/\/[^\s<]+[^\s<.,;:!?)\]'"]|\bwww\.[^\s<]+[^\s<.,;:!?)\]'"])/g

function findUrls(text) {
  return String(text || "").match(URL_RE) || []
}

// Plain text in, rich text out: escaped, links made clickable, line breaks
// and runs of spaces kept.
function linkify(text, linkColor) {
  var escaped = escapeHtml(text)
  var color = linkColor ? String(linkColor) : ""
  var html = escaped.replace(URL_RE, function(url) {
    var href = /^www\./.test(url) ? "http://" + url : url
    var inner = color ? '<span style="color:' + color + '">' + url + "</span>" : url
    return '<a href="' + href + '">' + inner + "</a>"
  })
  return '<span style="white-space:pre-wrap">' + html.replace(/\n/g, "<br>") + "</span>"
}

function codePoints(s) {
  var out = []
  s = String(s || "")
  for (var i = 0; i < s.length; i++) {
    var c = s.charCodeAt(i)
    if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      var d = s.charCodeAt(i + 1)
      if (d >= 0xdc00 && d <= 0xdfff) { out.push((c - 0xd800) * 0x400 + (d - 0xdc00) + 0x10000); i++; continue }
    }
    out.push(c)
  }
  return out
}

function isEmojiBase(cp) {
  return (cp >= 0x1f000 && cp <= 0x1faff) || (cp >= 0x2600 && cp <= 0x27bf) || (cp >= 0x2b00 && cp <= 0x2bff)
    || (cp >= 0x2190 && cp <= 0x21ff) || (cp >= 0x2300 && cp <= 0x23ff) || (cp >= 0x25a0 && cp <= 0x25ff)
    || cp === 0x00a9 || cp === 0x00ae || cp === 0x203c || cp === 0x2049 || cp === 0x2122 || cp === 0x2139
    || cp === 0x3030 || cp === 0x303d || cp === 0x3297 || cp === 0x3299
}

// Messages made of nothing but one to three emoji are shown large, as
// WhatsApp does. Returns the count (0 when it is not emoji-only).
function emojiOnlyCount(text) {
  var cps = codePoints(String(text || "").replace(/\s+/g, ""))
  if (cps.length === 0) return 0
  var clusters = 0
  var joined = false
  var pendingRegional = false
  for (var i = 0; i < cps.length; i++) {
    var cp = cps[i]
    if (cp === 0x200d) { joined = true; continue }
    if (cp === 0xfe0f || cp === 0xfe0e || cp === 0x20e3 || (cp >= 0x1f3fb && cp <= 0x1f3ff) || (cp >= 0xe0020 && cp <= 0xe007f)) continue
    // Keycaps: a digit, # or * followed by FE0F 20E3.
    var keycap = (cp === 0x23 || cp === 0x2a || (cp >= 0x30 && cp <= 0x39)) && cps[i + 1] === 0xfe0f && cps[i + 2] === 0x20e3
    if (!isEmojiBase(cp) && !keycap) return 0
    if (cp >= 0x1f1e6 && cp <= 0x1f1ff) {
      if (pendingRegional) { pendingRegional = false; continue }
      pendingRegional = true
    }
    if (!joined) clusters++
    joined = false
  }
  return clusters <= 3 ? clusters : 0
}

function formatSize(bytes) {
  var n = Number(bytes) || 0
  if (n <= 0) return ""
  if (n < 1024) return n + " B"
  if (n < 1024 * 1024) return Math.round(n / 1024) + " KB"
  return (n / 1024 / 1024).toFixed(n < 10 * 1024 * 1024 ? 1 : 0) + " MB"
}

function formatDuration(seconds) {
  var s = Math.max(0, Math.round(Number(seconds) || 0))
  return Math.floor(s / 60) + ":" + pad2(s % 60)
}

// Pictures shown in the bubble; audio and documents are a row instead.
function isVisualMedia(media) {
  return !!media && (media.type === "image" || media.type === "sticker" || media.type === "gif" || media.type === "video")
}

function isAutoDownload(media) {
  return !!media && (media.type === "image" || media.type === "sticker" || media.type === "gif")
}

// Box for a picture: aspect kept, fitted inside maxW x maxH, never tiny.
function mediaBox(media, maxW, maxH) {
  if (!media) return { w: 0, h: 0 }
  if (media.type === "sticker") {
    var side = Math.min(maxW, 150)
    return { w: side, h: side }
  }
  var w = Number(media.width) || 0
  var h = Number(media.height) || 0
  var aspect = w > 0 && h > 0 ? w / h : 4 / 3
  var bw = Math.min(maxW, 300)
  var bh = bw / aspect
  if (bh > maxH) { bh = maxH; bw = bh * aspect }
  if (bw < 120) { bw = 120; bh = Math.min(maxH, bw / aspect) }
  return { w: Math.round(bw), h: Math.round(bh) }
}

// What is displayed in the box right now: the real file once downloaded
// (the converted webp for a GIF), otherwise the embedded thumbnail.
function mediaSource(media) {
  if (!media) return ""
  if (media.type === "gif" && media.anim) return fileUrl(media.anim)
  if ((media.type === "image" || media.type === "sticker") && media.file) return fileUrl(media.file)
  if (media.thumb) return fileUrl(media.thumb)
  return ""
}

function mediaAnimated(media) {
  if (!media) return false
  return (media.type === "gif" && !!media.anim) || (media.type === "sticker" && media.animated === true && !!media.file)
}

function fileRowLabel(media) {
  if (!media) return ""
  if (media.type === "audio") return (media.voice ? "Voice message" : "Audio") + (media.seconds ? " · " + formatDuration(media.seconds) : "")
  return media.fileName || "Document"
}

function fileRowDetail(media) {
  if (!media) return ""
  var parts = []
  if (media.type === "document" && media.mime) {
    var sub = String(media.mime).split(";")[0].split("/").pop()
    if (sub && sub.length <= 12) parts.push(sub.toUpperCase())
  }
  var size = formatSize(media.size)
  if (size) parts.push(size)
  parts.push(media.file ? "click to open" : "click to download and open")
  return parts.join(" · ")
}

function fileRowGlyph(media) {
  if (!media) return GLYPH_DOCUMENT
  if (media.type === "audio") return media.voice ? GLYPH_MIC : GLYPH_MUSIC
  return GLYPH_DOCUMENT
}

// The text under a picture is its caption, not the "📷 Photo · …" label the
// chat list uses.
function bubbleText(body, media) {
  if (!media) return String(body || "")
  return String(media.caption || "")
}

// Row numbers by message id, so a burst of receipts or downloads does not walk
// the whole conversation for each one (reading a row is not free). The numbers
// are kept between lookups and thrown away when rows move; every hit is checked
// against the rows themselves, so a stale index can only cost one extra walk,
// never a wrong row. count() is how many rows there are, idAt(i) the id of row i.
function makeRowIndex() {
  var map = null
  return {
    forget: function() { map = null },
    // Only for the tests: whether the numbers are being kept right now.
    known: function() { return map !== null },
    find: function(id, count, idAt) {
      var n = count()
      if (map === null) {
        map = {}
        for (var i = 0; i < n; i++) map[idAt(i)] = i
      }
      var at = map[id]
      if (typeof at === "number" && at < n && idAt(at) === id) return at
      for (var j = n - 1; j >= 0; j--) {
        if (idAt(j) === id) {
          map = null   // the numbers no longer match the rows
          return j
        }
      }
      return -1
    }
  }
}

// ---- pinned chats
//
// Pins are local to this computer: an ordered list of chat JIDs in
// <data dir>/pins.json, written by the plugin and read by the daemon (so a
// pinned chat is always listed, however old).

function dataDir(env) {
  env = env || {}
  if (env.override) return String(env.override)
  var data = env.dataHome ? String(env.dataHome) : String(env.home || "") + "/.local/share"
  return data + "/omawhats"
}

function parsePins(text) {
  var v
  try { v = JSON.parse(String(text || "[]")) } catch (e) { return [] }
  if (!Array.isArray(v)) return []
  var out = []
  for (var i = 0; i < v.length; i++) {
    var j = String(v[i] || "")
    if (j !== "" && out.indexOf(j) === -1) out.push(j)
  }
  return out
}

function isPinned(pins, jid) {
  return Array.isArray(pins) && pins.indexOf(jid) !== -1
}

// Pinned chats first, in pin order, then the rest newest first as the daemon
// sent them. Chats are copied, not marked in place.
function applyPins(chats, pins) {
  if (!Array.isArray(chats)) return []
  if (!Array.isArray(pins) || pins.length === 0) return chats
  var byJid = {}
  for (var i = 0; i < chats.length; i++) byJid[chats[i].jid] = chats[i]
  var out = []
  for (var p = 0; p < pins.length; p++) {
    var c = byJid[pins[p]]
    if (!c) continue
    var copy = {}
    for (var k in c) copy[k] = c[k]
    copy.pinned = true
    out.push(copy)
  }
  for (var j = 0; j < chats.length; j++) if (pins.indexOf(chats[j].jid) === -1) out.push(chats[j])
  return out
}

function togglePin(pins, jid) {
  var out = Array.isArray(pins) ? pins.slice() : []
  if (!jid) return out
  var i = out.indexOf(jid)
  if (i === -1) out.push(jid)
  else out.splice(i, 1)
  return out
}

// Moves a pinned chat up (delta < 0) or down among the pins.
function movePin(pins, jid, delta) {
  var out = Array.isArray(pins) ? pins.slice() : []
  var i = out.indexOf(jid)
  if (i === -1) return out
  var to = Math.max(0, Math.min(out.length - 1, i + delta))
  if (to === i) return out
  out.splice(i, 1)
  out.splice(to, 0, jid)
  return out
}

// ---- older messages

// What the top of the conversation says, given how paging stands.
//   phase: "idle" | "local" (reading the local copy) | "phone" (waiting for
//   the phone) | "end" | "timeout" | "offline-end"
function historyHeader(phase) {
  switch (phase) {
  case "local": return "Loading older messages…"
  case "phone": return "Fetching older messages from your phone…"
  case "end": return "No older messages"
  case "timeout": return "Your phone did not answer. Scroll up or click here to try again."
  case "offline-end": return "Turn OmaWhats on to load older messages from your phone"
  }
  return ""
}

// ---- sending files
//
// JPEG and PNG pictures go as photos (unless "photos as files" is on);
// everything else — videos included — as a document. The daemon decides by
// the file's content; this is the same guess by name, for the tray.

var MAX_FILE_BYTES = 2000 * 1024 * 1024

function isPhotoName(path) {
  return /\.(jpe?g|png)$/i.test(String(path || ""))
}

// file:// URIs (drag and drop, a file manager's copy) to local paths.
function pathsFromUris(list) {
  var items = Array.isArray(list) ? list : String(list || "").split(/\r?\n/)
  var out = []
  for (var i = 0; i < items.length; i++) {
    var u = String(items[i] || "").trim()
    if (u === "" || u.charAt(0) === "#") continue
    if (u.indexOf("file://") !== 0) continue
    var p = u.substring(7)
    if (p.charAt(0) !== "/") p = p.substring(p.indexOf("/")) // file://host/path
    try { p = decodeURIComponent(p) } catch (e) { continue }
    if (p.length > 1 && out.indexOf(p) < 0) out.push(p)
  }
  return out
}

// `stat -L --printf '%s\t%F\t%n\n' …` output to files; folders and the like
// come back in "skipped".
function parseStat(text) {
  var files = [], skipped = []
  var lines = String(text || "").split("\n")
  for (var i = 0; i < lines.length; i++) {
    var parts = lines[i].split("\t")
    if (parts.length < 3) continue
    var path = parts.slice(2).join("\t")
    if (parts[1] === "regular file" || parts[1] === "regular empty file") {
      files.push({ path: path, name: baseName(path), size: Number(parts[0]) || 0, photo: isPhotoName(path) })
    } else {
      skipped.push(path)
    }
  }
  return { files: files, skipped: skipped }
}

// Adds files to the ones waiting to be sent, without repeats; returns the new
// list and why any were left out.
function addAttachments(current, files) {
  var list = (current || []).slice()
  var problems = []
  for (var i = 0; i < files.length; i++) {
    var f = files[i]
    var dup = false
    for (var j = 0; j < list.length; j++) if (list[j].path === f.path) dup = true
    if (dup) continue
    if (f.size <= 0) { problems.push(f.name + " is empty"); continue }
    if (f.size > MAX_FILE_BYTES) { problems.push(f.name + " is bigger than 2 GB"); continue }
    list.push(f)
  }
  return { list: list, problems: problems }
}

function attachmentsSummary(list, asFiles) {
  var photos = 0
  for (var i = 0; i < list.length; i++) if (list[i].photo && !asFiles) photos++
  var files = list.length - photos
  var parts = []
  if (photos) parts.push(photos + (photos === 1 ? " photo" : " photos"))
  if (files) parts.push(files + (files === 1 ? " file" : " files"))
  return parts.join(" and ")
}

// What a file looks like in the conversation while it is being sent (the
// daemon's copy replaces it).
function pendingMedia(file, caption, asFiles) {
  if (file.photo && !asFiles) return { type: "image", file: file.path, caption: caption || "" }
  return { type: "document", file: file.path, fileName: file.name, size: file.size, caption: caption || "" }
}

// What Ctrl+V does, from the clipboard's types (`wl-paste --list-types`):
// "files" (copied in a file manager), "image" (a screenshot, a copied
// picture) or "text".
function pasteKind(typesText) {
  var types = String(typesText || "").split(/\r?\n/).map(function(t) { return t.trim() })
  var has = function(t) { return types.indexOf(t) >= 0 }
  if (has("x-special/gnome-copied-files") || (has("text/uri-list") && !has("text/html"))) return "files"
  if (!has("text/plain") && !has("text/plain;charset=utf-8") && !has("UTF8_STRING") && pasteImageType(typesText) !== "") return "image"
  return "text"
}

function pasteImageType(typesText) {
  var types = String(typesText || "").split(/\r?\n/).map(function(t) { return t.trim() })
  if (types.indexOf("image/png") >= 0) return "image/png"
  if (types.indexOf("image/jpeg") >= 0) return "image/jpeg"
  return ""
}

// A local path as a URL: the only way a path should reach QML or a player.
// Names with "#", "?", "%" or spaces are kept whole ("#" would otherwise cut
// the name short) — pasted pictures have spaces, and a file we sent is shown
// from where it was picked, under whatever name it had.
function fileUrl(path) {
  var p = String(path || "")
  if (p === "") return ""
  return "file://" + p.split("/").map(encodeURIComponent).join("/")
}

function cacheDir(env) {
  env = env || {}
  if (env.override) return String(env.override)
  var cache = env.cacheHome ? String(env.cacheHome) : String(env.home || "") + "/.cache"
  return cache + "/omawhats/media"
}

// "Pasted 2026-09-19 16.30.05.png": the name a pasted picture is sent under
// when it goes as a file.
function pastedName(date, mimeType) {
  var d = date
  var stamp = d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) + " " +
    pad2(d.getHours()) + "." + pad2(d.getMinutes()) + "." + pad2(d.getSeconds())
  return "Pasted " + stamp + (mimeType === "image/jpeg" ? ".jpg" : ".png")
}

// ---- keyboard shortcuts, as listed in the client's help sheet

var SHORTCUTS = [
  { group: "Chats", keys: "Ctrl+F", action: "Search chats" },
  { group: "Chats", keys: "Alt+↑ / Alt+↓", action: "Previous / next chat" },
  { group: "Chats", keys: "Alt+1 … Alt+9", action: "Open pinned chat 1–9" },
  { group: "Chats", keys: "Ctrl+N", action: "New chat with a phone number" },
  { group: "Chats", keys: "Ctrl+P", action: "Pin / unpin the open chat" },
  { group: "Chats", keys: "Alt+Shift+↑ / ↓", action: "Move the pinned chat up / down" },
  { group: "Chats", keys: "Ctrl+W", action: "Close the open chat" },
  { group: "Messages", keys: "Enter", action: "Send" },
  { group: "Messages", keys: "Ctrl+E", action: "Emoji picker" },
  { group: "Messages", keys: "Ctrl+O", action: "Attach photos or files" },
  { group: "Messages", keys: "Ctrl+V", action: "Paste text, a picture or copied files" },
  { group: "Messages", keys: "Ctrl+D", action: "Send the attached photos as files (uncompressed)" },
  { group: "Messages", keys: "Esc", action: "Remove the attachments, or cancel the reply" },
  { group: "Messages", keys: "Tab", action: "Switch between search and message" },
  { group: "Messages", keys: "PgUp / PgDn", action: "Scroll (older messages load at the top)" },
  { group: "Messages", keys: "Alt+Home / Alt+End", action: "Oldest loaded / newest message" },
  { group: "Messages", keys: "Ctrl+↑", action: "Select messages with the keyboard" },
  { group: "Messages", keys: "Alt+P", action: "Play / pause the current audio" },
  { group: "Messages", keys: "Alt+S", action: "Audio speed 1× / 1.5× / 2×" },
  { group: "Selected message", keys: "↑ / ↓", action: "Previous / next message" },
  { group: "Selected message", keys: "R", action: "Reply to it" },
  { group: "Selected message", keys: "E", action: "React to it" },
  { group: "Selected message", keys: "Enter", action: "Open its photo, file or link; play its audio" },
  { group: "Selected message", keys: "Space", action: "Play / pause its audio" },
  { group: "Selected message", keys: "C", action: "Copy its text" },
  { group: "Selected message", keys: "Esc", action: "Back to typing" },
  { group: "Reactions", keys: "1 … 6", action: "👍 ❤️ 😂 😮 😢 🙏 (again takes it back)" },
  { group: "Reactions", keys: "← / → and Enter", action: "Pick a reaction" },
  { group: "Reactions", keys: "+ / Tab", action: "Any other emoji" },
  { group: "Emoji picker", keys: "type", action: "Search by name (heart, laugh, cat, …)" },
  { group: "Emoji picker", keys: "Arrows and Enter", action: "Pick an emoji" },
  { group: "Emoji picker", keys: "Esc", action: "Clear the search, or close" },
  { group: "Image viewer", keys: "← / →", action: "Previous / next image in the chat" },
  { group: "Image viewer", keys: "+ / − / wheel", action: "Zoom in / out" },
  { group: "Image viewer", keys: "0 / 1 / double-click", action: "Fit to window / actual size" },
  { group: "Image viewer", keys: "O", action: "Open in the system image viewer" },
  { group: "Image viewer", keys: "Esc", action: "Back to the chat" },
  { group: "OmaWhats", keys: "Ctrl+Shift+P", action: "Turn OmaWhats on / off" },
  { group: "OmaWhats", keys: "Ctrl+R", action: "Refresh; new QR code while linking" },
  { group: "OmaWhats", keys: "Ctrl+Shift+L", action: "Log out (unlink this computer)" },
  { group: "OmaWhats", keys: "Ctrl+Shift+W", action: "Switch between the popup and a window" },
  { group: "OmaWhats", keys: "F1 / Ctrl+/", action: "Show this list" },
  { group: "OmaWhats", keys: "Esc", action: "Close a dialog, clear the search, or close the popup" }
]

// The same list as rows for a Repeater: a header row before each group.
function shortcutRows() {
  var rows = []
  var last = ""
  for (var i = 0; i < SHORTCUTS.length; i++) {
    var s = SHORTCUTS[i]
    if (s.group !== last) { rows.push({ header: true, text: s.group, keys: "" }); last = s.group }
    rows.push({ header: false, text: s.action, keys: s.keys })
  }
  return rows
}

// The first link in a message, for opening a selected message from the
// keyboard: the preview card's URL, else the first URL in the text.
function firstLink(text, link) {
  if (link && link.url) return /^https?:\/\//.test(link.url) ? String(link.url) : "http://" + link.url
  var urls = findUrls(text)
  if (urls.length === 0) return ""
  return /^www\./.test(urls[0]) ? "http://" + urls[0] : urls[0]
}

// ---- audio played in the client

function isAudio(media) {
  return !!media && media.type === "audio"
}

function audioLabel(media) {
  if (!media) return ""
  if (media.voice) return "Voice message"
  return media.fileName || "Audio"
}

// "0:42" before playing; "0:12 / 0:42" while this audio is loaded.
function audioTime(posMs, durMs, active) {
  var dur = formatDuration((Number(durMs) || 0) / 1000)
  if (!active) return dur
  return formatDuration((Number(posMs) || 0) / 1000) + " / " + dur
}

var AUDIO_RATES = [1, 1.5, 2]

function nextRate(rate) {
  var i = AUDIO_RATES.indexOf(Number(rate))
  return AUDIO_RATES[(i + 1) % AUDIO_RATES.length]
}

function rateLabel(rate) {
  return String(Number(rate) || 1) + "×"
}

// ---- image viewer

// Pictures the in-client viewer shows; videos go to the system player.
function isViewable(media) {
  return !!media && (media.type === "image" || media.type === "sticker" || media.type === "gif")
}

// What the viewer displays: the full file when it is on disk (the converted
// webp for a GIF), else the embedded thumbnail, flagged as not full size.
function viewerSource(media) {
  if (!isViewable(media)) return { source: "", animated: false, full: false }
  if (media.type === "gif" && media.anim) return { source: fileUrl(media.anim), animated: true, full: true }
  if (media.type !== "gif" && media.file)
    return { source: fileUrl(media.file), animated: media.type === "sticker" && media.animated === true, full: true }
  return { source: media.thumb ? fileUrl(media.thumb) : "", animated: false, full: false }
}

// Scale that fits an image inside a box without enlarging it past 100 %,
// except that a small image is still allowed to fill a quarter of the box.
function fitScale(iw, ih, w, h) {
  iw = Number(iw) || 0
  ih = Number(ih) || 0
  if (iw <= 0 || ih <= 0 || w <= 0 || h <= 0) return 1
  var fit = Math.min(w / iw, h / ih)
  return Math.min(fit, Math.max(1, fit / 2))
}

// ---- replies

// The quoted message as one line.
function quoteLine(quote) {
  if (!quote) return ""
  var t = String(quote.text || "").replace(/\s+/g, " ").trim()
  return t !== "" ? t : "Message"
}

// Who wrote the quoted message; fallback is the chat's name, for a one-to-one
// chat whose quote does not say.
function quoteName(quote, fallback) {
  if (!quote) return ""
  if (quote.fromMe) return "You"
  if (quote.name) return String(quote.name)
  var user = String(quote.sender || "").split("@")[0]
  if (/^[0-9]{8,15}$/.test(user)) return formatPhone(user)
  return fallback ? String(fallback) : ""
}

// What the reply bar above the message box holds, from a message row.
function replyTarget(row) {
  if (!row) return null
  var media = parseJson(row.mediaJson)
  var text = bubbleText(row.body, media)
  return {
    id: String(row.mid || ""),
    name: row.fromMe ? "You" : String(row.senderName || ""),
    fromMe: row.fromMe === true,
    text: text !== "" ? text : String(row.body || ""),
    thumb: media && media.thumb ? String(media.thumb) : ""
  }
}

// ---- reactions

var QUICK_REACTIONS = ["👍", "❤️", "😂", "😮", "😢", "🙏"]

// Reactions grouped by emoji, the most given first (ties keep arrival order).
function reactionSummary(reactions) {
  var list = Array.isArray(reactions) ? reactions : []
  var groups = []
  var byEmoji = {}
  for (var i = 0; i < list.length; i++) {
    var r = list[i]
    if (!r || !r.emoji) continue
    var g = byEmoji[r.emoji]
    if (!g) {
      g = { emoji: String(r.emoji), count: 0, mine: false, names: [], order: groups.length }
      byEmoji[r.emoji] = g
      groups.push(g)
    }
    g.count += 1
    if (r.fromMe) g.mine = true
    g.names.push(r.fromMe ? "You" : String(r.name || "Someone"))
  }
  groups.sort(function(a, b) { return b.count - a.count || a.order - b.order })
  for (var j = 0; j < groups.length; j++) delete groups[j].order
  return groups
}

function myReaction(reactions) {
  var list = Array.isArray(reactions) ? reactions : []
  for (var i = 0; i < list.length; i++) if (list[i] && list[i].fromMe && list[i].emoji) return String(list[i].emoji)
  return ""
}

// The emoji to send when one is picked: picking your current one takes it back.
function reactionToggle(current, picked) {
  return current === picked ? "" : String(picked || "")
}

// The reactions as they will be once this account's is set to emoji (""
// removes it) — shown straight away, before the daemon confirms.
function withMyReaction(reactions, emoji) {
  var list = Array.isArray(reactions) ? reactions : []
  var out = []
  for (var i = 0; i < list.length; i++) if (list[i] && !list[i].fromMe) out.push(list[i])
  if (emoji) out.push({ emoji: String(emoji), sender: "", name: "You", fromMe: true })
  return out
}

// ---- emoji picker (the list is Omarchy's own, from its emoji plugin)

var FALLBACK_EMOJIS = [
  "😀 grinning smile happy", "😂 joy tears laugh", "🤣 rofl laugh", "😊 blush smile", "😍 heart eyes love",
  "😘 kiss", "😉 wink", "😎 cool sunglasses", "🤔 thinking", "😅 sweat smile", "😢 cry sad", "😭 sob cry",
  "😮 wow surprised open mouth", "😡 angry", "🙄 eye roll", "😴 sleep", "🥳 party", "🤗 hug", "🙏 pray thanks please",
  "👍 thumbs up like yes", "👎 thumbs down no", "👏 clap", "🙌 hands raised", "💪 muscle strong", "👋 wave hello bye",
  "👌 ok", "✌️ victory peace", "🤝 handshake", "❤️ heart love red", "💔 broken heart", "🔥 fire", "✨ sparkles",
  "🎉 tada party", "✅ check yes done", "❌ cross no", "⭐ star", "💯 hundred", "😆 laughing", "🥲 smile tear", "😬 grimace"
]

function fallbackEmojis() {
  var out = []
  for (var i = 0; i < FALLBACK_EMOJIS.length; i++) {
    var parts = FALLBACK_EMOJIS[i].split(" ")
    out.push({ e: parts[0], k: parts.slice(1).join(" ") })
  }
  return out
}

function parseEmojis(raw) {
  try {
    var data = JSON.parse(String(raw || ""))
    if (!Array.isArray(data)) return []
    var out = []
    for (var i = 0; i < data.length; i++) if (data[i] && data[i].e) out.push({ e: String(data[i].e), k: String(data[i].k || "") })
    return out
  } catch (e) {
    return []
  }
}

// Every word typed has to appear in the emoji's keywords.
function filterEmojis(list, query, limit) {
  var words = String(query || "").toLowerCase().split(/\s+/).filter(function(w) { return w !== "" })
  var max = limit > 0 ? limit : 2000
  var out = []
  for (var i = 0; i < list.length && out.length < max; i++) {
    var k = String(list[i].k || "").toLowerCase()
    var ok = true
    for (var j = 0; j < words.length && ok; j++) ok = k.indexOf(words[j]) >= 0
    if (ok) out.push(list[i])
  }
  return out
}

// What the picker grid shows: the matches for a search; without one, the
// recently used first, padded to whole rows so the full list starts on a row
// of its own.
function pickerList(all, recent, query, columns) {
  if (String(query || "").trim() !== "") return filterEmojis(all, query, 600)
  var out = []
  var rec = Array.isArray(recent) ? recent : []
  for (var i = 0; i < rec.length; i++) out.push({ e: String(rec[i]), k: "" })
  if (out.length > 0 && columns > 0) while (out.length % columns !== 0) out.push({ e: "", k: "" })
  return out.concat(all)
}

function pushRecent(recent, emoji, max) {
  var list = Array.isArray(recent) ? recent : []
  var out = [String(emoji)]
  for (var i = 0; i < list.length && out.length < (max || 24); i++) if (list[i] !== emoji) out.push(list[i])
  return out
}

function parseRecent(text) {
  try {
    var data = JSON.parse(String(text || ""))
    return Array.isArray(data) ? data.filter(function(x) { return typeof x === "string" && x !== "" }).slice(0, 24) : []
  } catch (e) {
    return []
  }
}
