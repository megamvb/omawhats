package main

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// The refresh button with no connection: the client hears back, instead of
// waiting for an answer that cannot come.
func TestResyncWithoutConnectionTellsTheClient(t *testing.T) {
	d := testDaemon(t)
	client, srvSide := net.Pipe()
	c := &conn{nc: srvSide}
	d.srv = &server{d: d, conns: map[*conn]struct{}{c: {}}}

	go d.resyncChat(testChat)

	client.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	var reply struct {
		Type    string `json:"type"`
		Chat    string `json:"chat"`
		Offline bool   `json:"offline"`
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Type != "resynced" || reply.Chat != testChat || !reply.Offline {
		t.Errorf("got %s", line)
	}
}

// The phone's answers to the refresh button and to paging back through a
// conversation arrive the same way, so a refresh must only settle the chats
// that asked for one — anything else belongs to the paging request, which is
// left hanging if this takes it.
func TestFinishResyncOnlyTakesItsOwn(t *testing.T) {
	d := testDaemon(t)
	if d.finishResync(testChat, 3, 50, false) {
		t.Error("settled an answer nobody asked for")
	}
	d.olderMu.Lock()
	d.resyncPending[testChat] = time.AfterFunc(time.Hour, func() {})
	d.olderMu.Unlock()
	if !d.finishResync(testChat, 3, 50, false) {
		t.Error("did not settle its own answer")
	}
	d.olderMu.Lock()
	left := len(d.resyncPending)
	d.olderMu.Unlock()
	if left != 0 {
		t.Errorf("%d requests still waiting", left)
	}
	if d.finishResync(testChat, 3, 50, false) {
		t.Error("settled the same request twice")
	}
}

// The phone answers a history request only when the chat is named the way it
// keeps it. A message stored here can carry the sender's @lid address instead,
// and asking with that got silence — every request for a one-to-one chat timed
// out until both callers went through here.
func TestHistoryAnchorNamesTheChatNotTheSender(t *testing.T) {
	info, err := historyAnchor(testChat, "M1", false, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Chat.String(); got != testChat {
		t.Errorf("chat = %q, want %q", got, testChat)
	}
	if info.IsGroup {
		t.Error("a person's chat was called a group")
	}
	if info.ID != "M1" || info.Timestamp.Unix() != 1000 {
		t.Errorf("anchor lost its message: %q at %v", info.ID, info.Timestamp.Unix())
	}

	group := "120363000000000000@g.us"
	if info, err = historyAnchor(group, "M2", true, 2000); err != nil {
		t.Fatal(err)
	}
	if !info.IsGroup || info.Chat.String() != group {
		t.Errorf("group anchor = %q, isGroup %v", info.Chat.String(), info.IsGroup)
	}
	if !info.IsFromMe {
		t.Error("lost that the anchor is a message of mine")
	}
}
