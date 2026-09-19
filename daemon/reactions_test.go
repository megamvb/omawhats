package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	testChat = "5511911112222@s.whatsapp.net"
	testMe   = "5511900000000@s.whatsapp.net"
)

// testDaemon is a daemon with a fresh database and no WhatsApp client: enough
// for everything that only touches the store.
func testDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OMAWHATS_DATA", dir)
	t.Setenv("OMAWHATS_CACHE", dir+"/cache")
	db, err := openDB(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Daemon{ctx: ctx, cancel: cancel, db: db, chatsDirty: make(chan struct{}, 1),
		work:         make(chan struct{}, 32),
		olderPending: map[string]*time.Timer{}, olderDone: map[string]bool{}}
}

func storeMsg(t *testing.T, d *Daemon, id string, fromMe bool, text string) {
	t.Helper()
	m := &Msg{Chat: testChat, ID: id, Sender: testChat, SenderName: "Ana", FromMe: fromMe, TS: 1000, Text: text, Kind: "text",
		rawChat: testChat, rawSender: testChat}
	if fromMe {
		m.Sender, m.SenderName, m.rawSender = testMe, "You", testMe
	}
	if _, err := insertMessage(d.ctx, d.db, m, true); err != nil {
		t.Fatal(err)
	}
}

func info(fromMe bool, ts int64) types.MessageInfo {
	chat, _ := types.ParseJID(testChat)
	sender := chat
	if fromMe {
		sender, _ = types.ParseJID(testMe)
	}
	return types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe},
		ID:            "R" + time.Unix(ts, 0).String(),
		PushName:      "Ana",
		Timestamp:     time.Unix(ts, 0),
	}
}

func reaction(target, emoji string, tsMs int64) *waE2E.ReactionMessage {
	return &waE2E.ReactionMessage{
		Key:               &waCommon.MessageKey{ID: proto.String(target), RemoteJID: proto.String(testChat)},
		Text:              proto.String(emoji),
		SenderTimestampMS: proto.Int64(tsMs),
	}
}

func reactionsOf(t *testing.T, d *Daemon, id string) []Reaction {
	t.Helper()
	m, err := getMessage(d.ctx, d.db, testChat, id)
	if err != nil {
		t.Fatal(err)
	}
	return m.Reactions
}

func TestReactionsStoreReplaceAndRemove(t *testing.T) {
	d := testDaemon(t)
	storeMsg(t, d, "M1", true, "hello")

	d.handleReaction(info(false, 10), reaction("M1", "👍", 10_000), true)
	got := reactionsOf(t, d, "M1")
	if len(got) != 1 || got[0].Emoji != "👍" || got[0].Name != "Ana" || got[0].FromMe {
		t.Fatalf("after first reaction: %+v", got)
	}

	// A newer reaction from the same person replaces it; an older copy
	// arriving late does not bring the old one back.
	d.handleReaction(info(false, 20), reaction("M1", "❤️", 20_000), true)
	d.handleReaction(info(false, 15), reaction("M1", "😂", 15_000), false)
	if got := reactionsOf(t, d, "M1"); len(got) != 1 || got[0].Emoji != "❤️" {
		t.Fatalf("after replace: %+v", got)
	}

	// Removal is an empty emoji, and it too wins only if newer.
	d.handleReaction(info(false, 30), reaction("M1", "", 30_000), true)
	if got := reactionsOf(t, d, "M1"); len(got) != 0 {
		t.Fatalf("after removal: %+v", got)
	}
	d.handleReaction(info(false, 25), reaction("M1", "🙏", 25_000), false)
	if got := reactionsOf(t, d, "M1"); len(got) != 0 {
		t.Fatalf("stale reaction came back: %+v", got)
	}
}

func TestReactionsInHistoryPage(t *testing.T) {
	d := testDaemon(t)
	storeMsg(t, d, "M1", false, "one")
	storeMsg(t, d, "M2", false, "two")
	d.handleReaction(info(true, 10), reaction("M2", "😂", 10_000), false)

	msgs, err := history(d.ctx, d.db, testChat, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || len(msgs[0].Reactions) != 0 || len(msgs[1].Reactions) != 1 {
		t.Fatalf("page: %+v", msgs)
	}
	if r := msgs[1].Reactions[0]; !r.FromMe || r.Name != "You" || r.Emoji != "😂" {
		t.Fatalf("own reaction: %+v", r)
	}
}

func TestReactionBeforeItsMessage(t *testing.T) {
	d := testDaemon(t)
	// Reactions can arrive before the message they are on (history chunks).
	d.handleReaction(info(false, 10), reaction("LATE", "🔥", 10_000), true)
	storeMsg(t, d, "LATE", false, "arrived later")
	if got := reactionsOf(t, d, "LATE"); len(got) != 1 || got[0].Emoji != "🔥" {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryReactions(t *testing.T) {
	d := testDaemon(t)
	storeMsg(t, d, "M1", false, "hi")
	chat, _ := types.ParseJID(testChat)
	d.historyReactions(testChat, "M1", chat, []*waWeb.Reaction{
		{Key: &waCommon.MessageKey{FromMe: proto.Bool(true)}, Text: proto.String("👍"), SenderTimestampMS: proto.Int64(5)},
		{Key: &waCommon.MessageKey{FromMe: proto.Bool(false)}, Text: proto.String("❤️"), SenderTimestampMS: proto.Int64(6)},
	})
	got := reactionsOf(t, d, "M1")
	if len(got) != 2 || !got[0].FromMe || got[1].FromMe || got[1].Sender != testChat {
		t.Fatalf("got %+v", got)
	}
}

func TestContextOf(t *testing.T) {
	ci := &waE2E.ContextInfo{StanzaID: proto.String("Q1"), Participant: proto.String(testChat)}
	for name, m := range map[string]*waE2E.Message{
		"text":  {ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("x"), ContextInfo: ci}},
		"image": {ImageMessage: &waE2E.ImageMessage{Caption: proto.String("x"), ContextInfo: ci}},
		"audio": {AudioMessage: &waE2E.AudioMessage{ContextInfo: ci}},
		"doc": {DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{ContextInfo: ci}}}},
	} {
		if got := contextOf(m); got.GetStanzaID() != "Q1" {
			t.Errorf("%s: got %v", name, got)
		}
	}
	// Forwarded messages carry a ContextInfo too, but no quoted message id.
	fwd := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}}}
	if contextOf(fwd) != nil || contextOf(&waE2E.Message{Conversation: proto.String("x")}) != nil {
		t.Error("found a quote where there is none")
	}
}

func TestQuoteOf(t *testing.T) {
	d := testDaemon(t)
	storeMsg(t, d, "ORIG", false, "the original")
	reply := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("answer"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("ORIG"),
			Participant:   proto.String(testChat),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("the original")},
		},
	}}
	q := d.quoteOf(testChat, reply)
	if q == nil || q.ID != "ORIG" || q.Text != "the original" || q.Name != "Ana" || q.FromMe {
		t.Fatalf("quote: %+v", q)
	}

	// Quoting a photo that is not stored: the label, with the sender's number.
	photo := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("GONE"),
			Participant:   proto.String("5511933334444@s.whatsapp.net"),
			QuotedMessage: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look")}},
		},
	}}
	q = d.quoteOf(testChat, photo)
	if q == nil || q.Text != "📷 Photo · look" || q.Kind != "image" || q.Name != "+5511933334444" {
		t.Fatalf("photo quote: %+v", q)
	}
}

func TestReplyContextAndStoredQuote(t *testing.T) {
	d := testDaemon(t)
	storeMsg(t, d, "ORIG", false, "question?")
	ci, q, err := d.replyContext(testChat, "ORIG")
	if err != nil {
		t.Fatal(err)
	}
	if ci.GetStanzaID() != "ORIG" || ci.GetParticipant() != testChat || ci.GetQuotedMessage().GetConversation() != "question?" {
		t.Fatalf("context: %v", ci)
	}
	if q.Name != "Ana" || q.Text != "question?" {
		t.Fatalf("quote: %+v", q)
	}

	// The quote is stored with the reply and comes back with it.
	m := &Msg{Chat: testChat, ID: "REPLY", Sender: testMe, SenderName: "You", FromMe: true, TS: 2000, Text: "answer",
		Kind: "text", Quote: q, rawChat: testChat, rawSender: testMe}
	if _, err := insertMessage(d.ctx, d.db, m, true); err != nil {
		t.Fatal(err)
	}
	got, err := getMessage(d.ctx, d.db, testChat, "REPLY")
	if err != nil || got.Quote == nil || got.Quote.ID != "ORIG" {
		t.Fatalf("stored reply: %+v %v", got, err)
	}

	if _, _, err := d.replyContext(testChat, "NOPE"); err == nil {
		t.Error("replied to a message that is not stored")
	}
	d.db.Exec(`UPDATE owa_message SET kind = 'revoked' WHERE id = 'ORIG'`)
	if _, _, err := d.replyContext(testChat, "ORIG"); err == nil {
		t.Error("replied to a deleted message")
	}
}

// A database last written by an older daemon (no quote column, no reaction
// table) must still be readable by the offline commands.
func TestReadOnlyOpenUpgradesOldSchema(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OMAWHATS_DATA", dir)
	old, err := sql.Open("sqlite", "file:"+dbPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE owa_chat (jid TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', is_group INTEGER NOT NULL DEFAULT 0,
			last_ts INTEGER NOT NULL DEFAULT 0, preview TEXT NOT NULL DEFAULT '', preview_from_me INTEGER NOT NULL DEFAULT 0,
			preview_sender TEXT NOT NULL DEFAULT '', unread INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE owa_message (chat TEXT NOT NULL, id TEXT NOT NULL, raw_chat TEXT NOT NULL, sender TEXT NOT NULL,
			raw_sender TEXT NOT NULL, sender_name TEXT NOT NULL DEFAULT '', from_me INTEGER NOT NULL, ts INTEGER NOT NULL,
			text TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'text', status TEXT NOT NULL DEFAULT '', edited INTEGER NOT NULL DEFAULT 0,
			read INTEGER NOT NULL DEFAULT 1, media TEXT NOT NULL DEFAULT '', media_proto BLOB, link TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (chat, id))`,
		`INSERT INTO owa_message (chat, id, raw_chat, sender, raw_sender, from_me, ts, text) VALUES ('c', 'm', 'c', 's', 's', 0, 1, 'old')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	db, err := openDB(true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	msgs, err := history(context.Background(), db, "c", 10, 0, "")
	if err != nil || len(msgs) != 1 || msgs[0].Text != "old" {
		t.Fatalf("history on an upgraded database: %+v %v", msgs, err)
	}
}
