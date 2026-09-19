package main

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// A burst of changes must become one listing, and while a history sync runs the
// list must go out far less often: each listing makes every client rebuild it.
func TestChatLoopCoalescesBursts(t *testing.T) {
	d := testDaemon(t)
	var sent atomic.Int32
	go d.chatLoopWith(func() { sent.Add(1) })

	for i := 0; i < 50; i++ {
		d.markChatsDirty()
	}
	time.Sleep(800 * time.Millisecond)
	if n := sent.Load(); n < 1 || n > 3 {
		t.Errorf("50 changes in a row gave %d listings, want a couple", n)
	}

	d.mu.Lock()
	d.state.Syncing = true
	d.mu.Unlock()
	sent.Store(0)
	for i := 0; i < 50; i++ {
		d.markChatsDirty()
	}
	time.Sleep(800 * time.Millisecond)
	if n := sent.Load(); n != 0 {
		t.Errorf("while syncing, %d listings went out within 800ms", n)
	}
	time.Sleep(1200 * time.Millisecond)
	if n := sent.Load(); n < 1 {
		t.Errorf("while syncing, nothing went out after 2s")
	}
}

func TestChatsDelay(t *testing.T) {
	if chatsDelay(true) <= chatsDelay(false) {
		t.Errorf("syncing should wait longer: %v vs %v", chatsDelay(true), chatsDelay(false))
	}
}

func unreadCount(t *testing.T, d *Daemon, chat string) int {
	t.Helper()
	var n int
	if err := d.db.QueryRowContext(d.ctx, `SELECT COUNT(*) FROM owa_message WHERE chat = ? AND read = 0`, chat).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// markRead takes batches longer than one statement can hold.
func TestMarkReadLongBatch(t *testing.T) {
	d := testDaemon(t)
	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = fmt.Sprintf("U%04d", i)
		m := &Msg{Chat: testChat, ID: ids[i], Sender: testChat, SenderName: "Ana", TS: int64(1000 + i), Text: "hi",
			Kind: "text", rawChat: testChat, rawSender: testChat}
		if _, err := insertMessage(d.ctx, d.db, m, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := unreadCount(t, d, testChat); got != 1000 {
		t.Fatalf("stored %d unread, want 1000", got)
	}
	markRead(d.ctx, d.db, testChat, ids[:900])
	if got := unreadCount(t, d, testChat); got != 100 {
		t.Errorf("%d still unread, want 100", got)
	}
	var read int
	d.db.QueryRowContext(d.ctx, `SELECT read FROM owa_message WHERE chat = ? AND id = ?`, testChat, "U0950").Scan(&read)
	if read != 0 {
		t.Errorf("U0950 was not in the batch and should still be unread")
	}
	markRead(d.ctx, d.db, testChat, nil)
	if got := unreadCount(t, d, testChat); got != 100 {
		t.Errorf("an empty batch changed something: %d unread", got)
	}
}

// One transaction for a whole conversation, counting only what was new, and the
// preview line left pointing at the newest message.
func TestStoreMessagesBatch(t *testing.T) {
	d := testDaemon(t)
	if err := ensureChat(d.ctx, d.db, testChat, false, "Ana"); err != nil {
		t.Fatal(err)
	}
	storeMsg(t, d, "A1", false, "first")

	batch := []*Msg{
		{Chat: testChat, ID: "A1", Sender: testChat, SenderName: "Ana", TS: 1000, Text: "first", Kind: "text",
			rawChat: testChat, rawSender: testChat},
		{Chat: testChat, ID: "A2", Sender: testChat, SenderName: "Ana", TS: 2000, Text: "second", Kind: "text",
			rawChat: testChat, rawSender: testChat},
		{Chat: testChat, ID: "A3", Sender: testMe, SenderName: "You", FromMe: true, TS: 3000, Text: "third", Kind: "text",
			rawChat: testChat, rawSender: testMe},
	}
	n, err := d.storeMessages(testChat, batch)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("inserted %d, want 2 (A1 was already stored)", n)
	}
	msgs, err := history(d.ctx, d.db, testChat, 80, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("stored %d messages, want 3", len(msgs))
	}
	var preview string
	var lastTS int64
	var fromMe int
	d.db.QueryRowContext(d.ctx, `SELECT preview, last_ts, preview_from_me FROM owa_chat WHERE jid = ?`, testChat).
		Scan(&preview, &lastTS, &fromMe)
	if preview != "third" || lastTS != 3000 || fromMe != 1 {
		t.Errorf("preview line not refreshed: %q %d %d", preview, lastTS, fromMe)
	}

	if n, err := d.storeMessages(testChat, nil); n != 0 || err != nil {
		t.Errorf("empty batch: %d %v", n, err)
	}
}
