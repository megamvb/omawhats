package main

import "testing"

func storedMessage(t *testing.T, d *Daemon, id string) (text, kind string, read int) {
	t.Helper()
	err := d.db.QueryRowContext(d.ctx,
		`SELECT text, kind, read FROM owa_message WHERE chat = ? AND id = ?`, testChat, id).Scan(&text, &kind, &read)
	if err != nil {
		t.Fatal(err)
	}
	return
}

// The placeholder left for a message that could not be read has to give way to
// the real thing when the sender or the phone answers, without losing the badge
// the placeholder already earned.
func TestWaitingPlaceholderIsReplaced(t *testing.T) {
	d := testDaemon(t)
	hole := &Msg{Chat: testChat, ID: "M1", Sender: testChat, SenderName: "Ana", TS: 1000,
		Text: waitingText, Kind: waitingKind, rawChat: testChat, rawSender: testChat}
	if _, err := insertMessage(d.ctx, d.db, hole, false); err != nil {
		t.Fatal(err)
	}

	real := &Msg{Chat: testChat, ID: "M1", Sender: testChat, SenderName: "Ana", TS: 1200,
		Text: "the message itself", Kind: "text", rawChat: testChat, rawSender: testChat}
	replaced, err := replaceWaiting(d.ctx, d.db, real)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("the placeholder was not replaced")
	}
	text, kind, read := storedMessage(t, d, "M1")
	if text != "the message itself" || kind != "text" {
		t.Errorf("stored %q/%q after the answer", text, kind)
	}
	if read != 0 {
		t.Errorf("read = %d: the message became read on its own", read)
	}

	// A second copy (a history chunk, say) finds no placeholder any more.
	if replaced, err = replaceWaiting(d.ctx, d.db, real); err != nil {
		t.Fatal(err)
	} else if replaced {
		t.Error("replaced a message that was already whole")
	}
}

// Anything that is not a placeholder must survive a re-delivery untouched: the
// stored copy may have been edited or had its media filled in since.
func TestReplaceWaitingLeavesRealMessagesAlone(t *testing.T) {
	d := testDaemon(t)
	storeMsg(t, d, "M2", false, "as it was sent")
	older := &Msg{Chat: testChat, ID: "M2", Sender: testChat, SenderName: "Ana", TS: 1,
		Text: "an older copy", Kind: "text", rawChat: testChat, rawSender: testChat}
	if replaced, err := replaceWaiting(d.ctx, d.db, older); err != nil {
		t.Fatal(err)
	} else if replaced {
		t.Error("overwrote a message that was never a placeholder")
	}
	if text, _, _ := storedMessage(t, d, "M2"); text != "as it was sent" {
		t.Errorf("stored text is now %q", text)
	}
}
