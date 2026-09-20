// node tests/run.js — exercises Model.js without QML.
const fs = require("fs")
const path = require("path")
const src = fs.readFileSync(path.join(__dirname, "..", "Model.js"), "utf8").replace(/^\.pragma library\s*/, "")
const M = new Function(src + "\nreturn { " + [...src.matchAll(/^(?:function|var) (\w+)/gm)].map(m => m[1]).join(", ") + " }")()

let failed = 0, passed = 0
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want)
  if (g === w) passed++
  else { failed++; console.log(`FAIL ${name}\n  got:  ${g}\n  want: ${w}`) }
}

// parsing
eq("parseLine ok", M.parseLine('{"type":"pong"}'), { type: "pong" })
eq("parseLine junk", M.parseLine("not json"), null)
eq("parseLine untyped", M.parseLine('{"a":1}'), null)
eq("parseLines", M.parseLines('{"type":"a"}\n\nxx\n{"type":"b"}\n').map(o => o.type), ["a", "b"])
eq("normalizeState fills", M.normalizeState({ state: "connected", qr: null }).qr, [])
eq("normalizeState keeps", M.normalizeState({ state: "connected" }).state, "connected")

// socket path mirrors paths.go
eq("socket runtime", M.socketPath({ runtimeDir: "/run/user/1000" }), "/run/user/1000/omawhats.sock")
eq("socket override", M.socketPath({ override: "/x.sock", runtimeDir: "/r" }), "/x.sock")
eq("socket fallback", M.socketPath({ home: "/h" }), "/h/.local/share/omawhats/daemon.sock")

eq("dirName", M.dirName("/run/user/1000/omawhats.sock"), "/run/user/1000")
eq("baseName", M.baseName("/run/user/1000/omawhats.sock") + ".pid", "omawhats.sock.pid")

// unread
eq("totalUnread", M.totalUnread([{ unread: 2 }, { unread: "3" }, { unread: -1 }, {}]), 5)
eq("badge none", M.unreadBadge(0), "")
eq("badge big", M.unreadBadge(150), "99+")

// labels
eq("phone BR mobile", M.formatPhone("5511987654321"), "+55 11 98765-4321")
eq("phone BR landline", M.formatPhone("551133334444"), "+55 11 3333-4444")
eq("phone other", M.formatPhone("14155550100"), "+14155550100")
eq("label offline paired", M.stateLabel({ paired: true }, false), "Off")
eq("label offline unpaired", M.stateLabel({ paired: false }, false), "Off · not linked")
eq("label syncing", M.stateLabel({ state: "connected", syncing: true }, true), "Connected · syncing…")
eq("online", M.isOnline({ state: "connected" }, true), true)
eq("online needs live", M.isOnline({ state: "connected" }, false), false)
eq("alarm", M.isAlarm({ state: "error" }, true), true)
eq("pairing", M.needsPairing({ state: "pair-timeout" }, true), true)

// times (local time zone, so build inputs from local dates)
const now = new Date(2026, 8, 19, 15, 0).getTime()
const at = (y, mo, d, h, mi) => new Date(y, mo, d, h, mi).getTime() / 1000
eq("listTime today", M.listTime(at(2026, 8, 19, 9, 5), now), "09:05")
eq("listTime yesterday", M.listTime(at(2026, 8, 18, 23, 0), now), "Yesterday")
eq("listTime week", M.listTime(at(2026, 8, 15, 10, 0), now), "Tue")
eq("listTime year", M.listTime(at(2026, 0, 2, 10, 0), now), "Jan 2")
eq("listTime old", M.listTime(at(2025, 11, 31, 10, 0), now), "Dec 31, 2025")
eq("dayLabel", M.dayLabel(at(2026, 7, 3, 10, 0), now), "August 3")
eq("differentDay", M.differentDay(at(2026, 8, 19, 0, 1), at(2026, 8, 18, 23, 59)), true)
eq("sameDay", M.differentDay(at(2026, 8, 19, 0, 1), at(2026, 8, 19, 23, 59)), false)

// chats
const chats = [
  { jid: "5511987654321@s.whatsapp.net", name: "João Ávila", preview: "oi", previewFromMe: false },
  { jid: "120363@g.us", name: "Família", group: true, preview: "bom dia", previewSender: "Ana" },
  { jid: "5521999998888@s.whatsapp.net", name: "", preview: "ok", previewFromMe: true },
]
eq("chatName fallback", M.chatName(chats[2]), "+55 21 99999-8888")
eq("preview group", M.chatPreview(chats[1]), "Ana: bom dia")
eq("preview one line", M.chatPreview({ preview: "a\n\n  b\tc" }), "a b c")
eq("preview mine", M.chatPreview(chats[2]), "You: ok")
eq("filter accent", M.filterChats(chats, "joao avila").length, 1)
eq("filter number", M.filterChats(chats, "2199999").map(c => c.jid), ["5521999998888@s.whatsapp.net"])
eq("filter empty", M.filterChats(chats, "  ").length, 3)
eq("findChat", M.findChat(chats, "120363@g.us").name, "Família")

eq("jidFromPhone", M.jidFromPhone("+55 (11) 98765-4321"), "5511987654321@s.whatsapp.net")
eq("jidFromPhone short", M.jidFromPhone("12345"), "")

eq("startArgs default", M.startArgs({}), ["start"])
eq("startArgs off", M.startArgs({ notifications: false, readReceipts: false }), ["start", "--no-notify", "--no-read-receipts"])

eq("statusGlyph read", M.statusGlyph("read"), "\u{f012d}")

const shellJson = JSON.stringify({ bar: { layout: { left: [{ id: "x" }], right: [{ id: "megamvb.omawhats", notifications: false }] } } })
eq("widgetSettings", M.widgetSettings(shellJson, "megamvb.omawhats").notifications, false)
eq("widgetSettings missing", M.widgetSettings(shellJson, "nope"), {})
eq("widgetSettings junk", M.widgetSettings("{", "x"), {})

// rich text
eq("linkify plain", M.linkify("a<b"), '<span style="white-space:pre-wrap">a&lt;b</span>')
eq("linkify url", M.linkify("veja https://x.com/a?b=1&c=2."),
  '<span style="white-space:pre-wrap">veja <a href="https://x.com/a?b=1&amp;c=2">https://x.com/a?b=1&amp;c=2</a>.</span>')
eq("linkify www color", M.linkify("www.ex.com", "#ff0000"),
  '<span style="white-space:pre-wrap"><a href="http://www.ex.com"><span style="color:#ff0000">www.ex.com</span></a></span>')
eq("linkify newline", M.linkify("a\nb"), '<span style="white-space:pre-wrap">a<br>b</span>')
eq("linkify no js", M.linkify('javascript:alert(1) "x"'), '<span style="white-space:pre-wrap">javascript:alert(1) &quot;x&quot;</span>')
eq("findUrls", M.findUrls("a http://a.b, e https://c.d/e)"), ["http://a.b", "https://c.d/e"])

eq("emoji one", M.emojiOnlyCount("😂"), 1)
eq("emoji three spaced", M.emojiOnlyCount("😂 👍🏽 ❤️"), 3)
eq("emoji four", M.emojiOnlyCount("😂😂😂😂"), 0)
eq("emoji zwj family", M.emojiOnlyCount("👨‍👩‍👧"), 1)
eq("emoji flag", M.emojiOnlyCount("🇧🇷"), 1)
eq("emoji two flags", M.emojiOnlyCount("🇧🇷🇵🇹"), 2)
eq("emoji keycap", M.emojiOnlyCount("1️⃣"), 1)
eq("emoji with text", M.emojiOnlyCount("oi 😂"), 0)
eq("emoji digit", M.emojiOnlyCount("1"), 0)
eq("emoji empty", M.emojiOnlyCount("  "), 0)

eq("size KB", M.formatSize(2048), "2 KB")
eq("size MB", M.formatSize(1.5 * 1024 * 1024), "1.5 MB")
eq("duration", M.formatDuration(125), "2:05")

eq("box landscape", M.mediaBox({ type: "image", width: 1600, height: 900 }, 500, 320), { w: 300, h: 169 })
eq("box portrait", M.mediaBox({ type: "image", width: 900, height: 1600 }, 500, 320), { w: 180, h: 320 })
eq("box unknown", M.mediaBox({ type: "video" }, 500, 320), { w: 300, h: 225 })
eq("box sticker", M.mediaBox({ type: "sticker", width: 512, height: 512 }, 500, 320), { w: 150, h: 150 })

eq("source thumb", M.mediaSource({ type: "image", thumb: "/c/a.thumb.jpg" }), "file:///c/a.thumb.jpg")
eq("source file", M.mediaSource({ type: "image", thumb: "/t", file: "/c/a.jpg" }), "file:///c/a.jpg")
eq("source gif anim", M.mediaSource({ type: "gif", thumb: "/t", file: "/c/a.mp4", anim: "/c/a.gif.webp" }), "file:///c/a.gif.webp")
eq("source video keeps thumb", M.mediaSource({ type: "video", thumb: "/t", file: "/c/a.mp4" }), "file:///t")
eq("animated sticker", M.mediaAnimated({ type: "sticker", animated: true, file: "/f" }), true)
eq("static sticker", M.mediaAnimated({ type: "sticker", animated: false, file: "/f" }), false)

eq("bubbleText caption", M.bubbleText("📷 Photo · oi", { caption: "oi" }), "oi")
eq("bubbleText plain", M.bubbleText("oi", null), "oi")
eq("fileRow voice", M.fileRowLabel({ type: "audio", voice: true, seconds: 42 }), "Voice message · 0:42")
eq("fileRow detail", M.fileRowDetail({ type: "document", mime: "application/pdf", size: 2048 }), "PDF · 2 KB · click to download and open")

// pins
const pc = [{ jid: "a" }, { jid: "b" }, { jid: "c" }, { jid: "d" }]
eq("parsePins", M.parsePins('["b","b","",3]'), ["b", "3"])
eq("parsePins junk", M.parsePins("{"), [])
eq("applyPins none", M.applyPins(pc, []).map(c => c.jid), ["a", "b", "c", "d"])
eq("applyPins order", M.applyPins(pc, ["c", "x", "a"]).map(c => c.jid + (c.pinned ? "*" : "")), ["c*", "a*", "b", "d"])
eq("applyPins no mutation", pc[2].pinned, undefined)
eq("togglePin add", M.togglePin(["a"], "b"), ["a", "b"])
eq("togglePin remove", M.togglePin(["a", "b"], "a"), ["b"])
eq("movePin up", M.movePin(["a", "b", "c"], "c", -1), ["a", "c", "b"])
eq("movePin clamp", M.movePin(["a", "b"], "a", -1), ["a", "b"])
eq("movePin down", M.movePin(["a", "b", "c"], "a", 1), ["b", "a", "c"])
eq("isPinned", M.isPinned(["a"], "a"), true)
eq("dataDir", M.dataDir({ home: "/h" }), "/h/.local/share/omawhats")
eq("dataDir override", M.dataDir({ override: "/d", home: "/h" }), "/d")

// paging and help
eq("historyHeader phone", M.historyHeader("phone"), "Fetching older messages from your phone…")
eq("historyHeader idle", M.historyHeader("idle"), "")
eq("shortcutRows header first", M.shortcutRows()[0], { header: true, text: "Chats", keys: "" })
eq("shortcutRows count", M.shortcutRows().filter(r => !r.header).length, M.SHORTCUTS.length)
eq("firstLink card", M.firstLink("x", { url: "ex.com" }), "http://ex.com")
eq("firstLink text", M.firstLink("see www.a.com and https://b.c", null), "http://www.a.com")
eq("firstLink none", M.firstLink("nothing", null), "")

// audio
eq("audioLabel voice", M.audioLabel({ type: "audio", voice: true }), "Voice message")
eq("audioLabel file", M.audioLabel({ type: "audio", fileName: "song.mp3" }), "song.mp3")
eq("audioTime idle", M.audioTime(0, 42000, false), "0:42")
eq("audioTime active", M.audioTime(12400, 42000, true), "0:12 / 0:42")
eq("nextRate", [M.nextRate(1), M.nextRate(1.5), M.nextRate(2), M.nextRate(7)], [1.5, 2, 1, 1])
eq("rateLabel", M.rateLabel(1.5), "1.5×")
eq("isAudio", M.isAudio({ type: "audio" }) && !M.isAudio({ type: "document" }), true)

// image viewer
eq("viewable", [M.isViewable({ type: "image" }), M.isViewable({ type: "video" }), M.isViewable(null)], [true, false, false])
eq("viewerSource full", M.viewerSource({ type: "image", file: "/a.jpg", thumb: "/t" }), { source: "file:///a.jpg", animated: false, full: true })
eq("viewerSource thumb", M.viewerSource({ type: "image", thumb: "/t" }), { source: "file:///t", animated: false, full: false })
eq("viewerSource gif", M.viewerSource({ type: "gif", file: "/a.mp4", anim: "/a.webp" }), { source: "file:///a.webp", animated: true, full: true })
eq("viewerSource gif unconverted", M.viewerSource({ type: "gif", file: "/a.mp4", thumb: "/t" }).full, false)
eq("viewerSource sticker", M.viewerSource({ type: "sticker", file: "/s.webp", animated: true }).animated, true)

// A copy already downloaded is what makes asking for it again a forced ask.
eq("hasCachedFile none", M.hasCachedFile({ type: "image", thumb: "/t" }), false)
eq("hasCachedFile image", M.hasCachedFile({ type: "image", file: "/a.jpg" }), true)
eq("hasCachedFile gif unconverted", M.hasCachedFile({ type: "gif", file: "/a.mp4" }), false)
eq("hasCachedFile gif", M.hasCachedFile({ type: "gif", file: "/a.mp4", anim: "/a.webp" }), true)
eq("hasCachedFile nothing", M.hasCachedFile(null), false)

// The line in the empty half of the window: what is running, on both sides.
eq("versionLine both", M.versionLine("0.6.3", { version: "0.6.3" }, true), "OmaWhats 0.6.3 · daemon 0.6.3")
eq("versionLine daemon behind", M.versionLine("0.6.3", { version: "0.6.1" }, true), "OmaWhats 0.6.3 · daemon 0.6.1")
eq("versionLine daemon too old to say", M.versionLine("0.6.3", {}, true), "OmaWhats 0.6.3 · daemon from an older install")
eq("versionLine off", M.versionLine("0.6.3", { version: "0.6.3" }, false), "OmaWhats 0.6.3 · daemon off")
eq("versionLine without a manifest", M.versionLine("", null, false), "OmaWhats · daemon off")
eq("fitScale big", M.fitScale(4000, 3000, 800, 600), 0.2)
eq("fitScale small", M.fitScale(100, 100, 800, 600), 3)
eq("fitScale mid", M.fitScale(500, 400, 800, 600), 1)

// replies
eq("quoteLine folds", M.quoteLine({ text: "a\n  b" }), "a b")
eq("quoteLine empty", M.quoteLine({ text: "" }), "Message")
eq("quoteName me", M.quoteName({ fromMe: true, name: "X" }), "You")
eq("quoteName name", M.quoteName({ name: "Ana" }), "Ana")
eq("quoteName phone", M.quoteName({ sender: "5511987654321@s.whatsapp.net" }), "+55 11 98765-4321")
eq("quoteName fallback", M.quoteName({ sender: "123@lid" }, "Bob"), "Bob")
eq("replyTarget", M.replyTarget({ mid: "A", fromMe: false, senderName: "Ana", body: "📷 Photo · hi", mediaJson: '{"type":"image","caption":"hi","thumb":"/t"}' }),
  { id: "A", name: "Ana", fromMe: false, text: "hi", thumb: "/t" })

// reactions
const rs = [
  { emoji: "❤️", name: "Ana" }, { emoji: "👍", name: "Bob" }, { emoji: "👍", fromMe: true, name: "You" }, { emoji: "" }
]
eq("reactionSummary", M.reactionSummary(rs), [
  { emoji: "👍", count: 2, mine: true, names: ["Bob", "You"] },
  { emoji: "❤️", count: 1, mine: false, names: ["Ana"] }
])
eq("reactionSummary empty", M.reactionSummary(null), [])
eq("myReaction", M.myReaction(rs), "👍")
eq("reactionToggle same", M.reactionToggle("👍", "👍"), "")
eq("reactionToggle other", M.reactionToggle("👍", "❤️"), "❤️")
eq("withMyReaction set", M.withMyReaction(rs, "😂").map(r => r.emoji), ["❤️", "👍", "", "😂"])
eq("withMyReaction clear", M.withMyReaction(rs, "").filter(r => r.fromMe).length, 0)

// emoji picker
const em = [{ e: "😀", k: "grinning face smile" }, { e: "❤️", k: "red heart love" }, { e: "🐱", k: "cat face" }]
eq("parseEmojis", M.parseEmojis('[{"e":"😀","k":"x"},{"k":"no emoji"}]'), [{ e: "😀", k: "x" }])
eq("parseEmojis junk", M.parseEmojis("nope"), [])
eq("filterEmojis words", M.filterEmojis(em, "face cat").map(x => x.e), ["🐱"])
eq("filterEmojis case", M.filterEmojis(em, "HEART").map(x => x.e), ["❤️"])
eq("pickerList recent padded", M.pickerList(em, ["❤️"], "", 4).map(x => x.e), ["❤️", "", "", "", "😀", "❤️", "🐱"])
eq("pickerList search", M.pickerList(em, ["❤️"], "smile", 4).map(x => x.e), ["😀"])
eq("pushRecent", M.pushRecent(["a", "b", "c"], "b", 3), ["b", "a", "c"])
eq("pushRecent cap", M.pushRecent(["a", "b", "c"], "d", 3), ["d", "a", "b"])
eq("parseRecent", M.parseRecent('["a", 3, ""]'), ["a"])
eq("fallbackEmojis", M.fallbackEmojis()[0], { e: "😀", k: "grinning smile happy" })
eq("shortcut groups not repeated", (() => { const h = M.shortcutRows().filter(r => r.header).map(r => r.text); return h.length === new Set(h).size })(), true)

// sending files
eq("isPhotoName", ["a.JPG", "b.jpeg", "c.png", "d.webp", "e.pdf", "png"].map(M.isPhotoName), [true, true, true, false, false, false])
eq("pathsFromUris list", M.pathsFromUris("file:///home/m/My%20File.pdf\r\n# comment\nhttps://x.y/z\nfile:///home/m/a.png\nfile:///home/m/a.png\n"),
  ["/home/m/My File.pdf", "/home/m/a.png"])
eq("pathsFromUris urls + host", M.pathsFromUris(["file://localhost/tmp/x%C3%A7.txt", "file:///bad%E0%A4%A"]), ["/tmp/xç.txt"])
const st = M.parseStat("12\tregular file\t/h/a.png\n0\tregular empty file\t/h/e.txt\n4096\tdirectory\t/h/dir\n7\tregular file\t/h/tab\there.pdf\n")
eq("parseStat files", st.files.map(f => [f.name, f.size, f.photo]), [["a.png", 12, true], ["e.txt", 0, false], ["tab\there.pdf", 7, false]])
eq("parseStat skipped", st.skipped, ["/h/dir"])
const added = M.addAttachments([{ path: "/h/a.png", name: "a.png", size: 12 }], st.files.concat([{ path: "/h/big.mkv", name: "big.mkv", size: 3e9 }]))
eq("addAttachments", added.list.map(f => f.name), ["a.png", "tab\there.pdf"])
eq("addAttachments problems", added.problems, ["e.txt is empty", "big.mkv is bigger than 2 GB"])
const tray = [{ photo: true }, { photo: true }, { photo: false }]
eq("attachmentsSummary", M.attachmentsSummary(tray, false), "2 photos and 1 file")
eq("attachmentsSummary as files", M.attachmentsSummary(tray, true), "3 files")
eq("attachmentsSummary one", M.attachmentsSummary([{ photo: true }], false), "1 photo")
eq("pendingMedia photo", M.pendingMedia({ path: "/a.png", name: "a.png", size: 3, photo: true }, "hi", false), { type: "image", file: "/a.png", caption: "hi" })
eq("pendingMedia as file", M.pendingMedia({ path: "/a.png", name: "a.png", size: 3, photo: true }, "", true).type, "document")
eq("paste screenshot", M.pasteKind("image/png\n"), "image")
eq("paste nautilus", M.pasteKind("x-special/gnome-copied-files\ntext/uri-list\ntext/plain;charset=utf-8\n"), "files")
eq("paste browser image", M.pasteKind("text/html\nimage/png\n"), "image")
eq("paste browser link", M.pasteKind("text/html\ntext/uri-list\ntext/plain\n"), "text")
eq("paste text", M.pasteKind("text/plain;charset=utf-8\nUTF8_STRING\n"), "text")
eq("paste webp only", M.pasteKind("image/webp\n"), "text")
eq("pasteImageType", M.pasteImageType("image/jpeg\nimage/png"), "image/png")
eq("fileUrl", M.fileUrl("/h/a b#1?.png"), "file:///h/a%20b%231%3F.png")
eq("fileUrl empty", M.fileUrl(""), "")
// Every local path shown in the client goes through fileUrl: a photo we sent
// is displayed from where it was picked, and a pasted picture has spaces.
eq("mediaSource photo", M.mediaSource({ type: "image", file: "/h/My Pics/a#1.png" }), "file:///h/My%20Pics/a%231.png")
eq("mediaSource thumb", M.mediaSource({ type: "video", thumb: "/c/media/3A x.thumb.jpg" }), "file:///c/media/3A%20x.thumb.jpg")
eq("mediaSource gif", M.mediaSource({ type: "gif", anim: "/c/m/g #2.gif.webp" }), "file:///c/m/g%20%232.gif.webp")
eq("mediaSource none", M.mediaSource({ type: "document", fileName: "x.pdf" }), "")
eq("viewerSource full", M.viewerSource({ type: "image", file: "/h/a b#1.png" }).source, "file:///h/a%20b%231.png")
eq("viewerSource thumb", M.viewerSource({ type: "image", thumb: "/c/m/t 1.jpg" }).source, "file:///c/m/t%201.jpg")
eq("viewerSource thumbless", M.viewerSource({ type: "image" }).source, "")
eq("cacheDir", M.cacheDir({ home: "/h" }), "/h/.cache/omawhats/media")
eq("cacheDir xdg", M.cacheDir({ cacheHome: "/c", home: "/h" }), "/c/omawhats/media")
eq("pastedName", M.pastedName(new Date(2026, 8, 19, 16, 30, 5), "image/png"), "Pasted 2026-09-19 16.30.05.png")

// ---- row numbers by message id (Model.makeRowIndex)

function rowsOf(ids) {
  var reads = 0
  return {
    ids: ids,
    reads: function() { return reads },
    resetReads: function() { reads = 0 },
    count: function() { return ids.length },
    idAt: function(i) { reads++; return ids[i] }
  }
}
function find(idx, rows, id) { return idx.find(id, rows.count, rows.idAt) }

{
  const ids = []
  for (let i = 0; i < 100; i++) ids.push("M" + i)
  const rows = rowsOf(ids)
  const idx = M.makeRowIndex()
  eq("rowIndex finds a row", find(idx, rows, "M60"), 60)
  eq("rowIndex builds once, walking every row", rows.reads() >= 100, true)
  rows.resetReads()
  eq("rowIndex second lookup", find(idx, rows, "M3"), 3)
  eq("rowIndex second lookup reads one row", rows.reads(), 1)
  eq("rowIndex unknown id", find(idx, rows, "nope"), -1)

  // A page inserted at the top moves every row; forgetting is what the client does.
  rows.ids.unshift("O2", "O1")
  idx.forget()
  rows.resetReads()
  eq("rowIndex after a page, forgotten", find(idx, rows, "M3"), 5)
  eq("rowIndex rebuilt after forgetting", rows.reads() >= 102, true)

  // Not forgetting must still never answer a wrong row: the hit is checked.
  rows.ids.unshift("O4", "O3")
  eq("rowIndex stale but right", find(idx, rows, "M3"), 7)
  eq("rowIndex threw the stale numbers away", idx.known(), false)
  eq("rowIndex right again afterwards", find(idx, rows, "O3"), 1)

  // An id replaced in place (a placeholder becoming the real message).
  rows.ids[rows.ids.length - 1] = "REAL"
  eq("rowIndex renamed row", find(idx, rows, "REAL"), rows.ids.length - 1)
  eq("rowIndex old id is gone", find(idx, rows, "M99"), -1)

  // Odd ids must not find a row through the prototype chain.
  const plain = rowsOf(["a", "b"])
  const idx2 = M.makeRowIndex()
  eq("rowIndex constructor", find(idx2, plain, "constructor"), -1)
  eq("rowIndex __proto__", find(idx2, plain, "__proto__"), -1)
  eq("rowIndex empty model", find(M.makeRowIndex(), rowsOf([]), "x"), -1)
}

console.log(`${passed} passed, ${failed} failed`)
process.exit(failed ? 1 : 0)
