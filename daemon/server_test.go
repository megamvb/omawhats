package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Only so many commands run at once, and a slot comes back when one is done.
func TestRunBoundsCommands(t *testing.T) {
	d := testDaemon(t)
	d.work = make(chan struct{}, 1)
	release := make(chan struct{})
	if !d.run(func() { <-release }) {
		t.Fatal("the first command should have run")
	}
	if d.run(func() {}) {
		t.Error("the second should have been refused while the slot is taken")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for !d.run(func() {}) {
		if time.Now().After(deadline) {
			t.Fatal("the slot never came back")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A refused command is answered, in the shape that command's answer has, so the
// client is never left waiting.
func TestDispatchAnswersWhenBusy(t *testing.T) {
	d := testDaemon(t)
	d.srv = &server{d: d, conns: map[*conn]struct{}{}}
	d.work = make(chan struct{}) // no room, ever

	cases := []struct {
		cmd, replyType string
	}{
		{"send", "sent"},
		{"react", "reacted"},
		{"check", "check"},
	}
	for _, tc := range cases {
		client, srvSide := net.Pipe()
		c := &conn{nc: srvSide}
		go d.dispatch(c, Request{Cmd: tc.cmd, Chat: testChat, Text: "hi", ID: "M1", Req: "r1"})
		client.SetReadDeadline(time.Now().Add(3 * time.Second))
		line, err := bufio.NewReader(client).ReadBytes('\n')
		if err != nil {
			t.Fatalf("%s: no answer: %v", tc.cmd, err)
		}
		var reply struct {
			Type  string `json:"type"`
			Req   string `json:"req"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatalf("%s: %v (%s)", tc.cmd, err, line)
		}
		if reply.Type != tc.replyType || reply.Req != "r1" || reply.OK || !strings.Contains(reply.Error, "busy") {
			t.Errorf("%s answered %s", tc.cmd, line)
		}
		client.Close()
		srvSide.Close()
	}
}

// Writes from several goroutines at once must not fail: SQLite takes one writer
// at a time and the pool plus busy_timeout have to absorb that.
func TestConcurrentWrites(t *testing.T) {
	d := testDaemon(t)
	if got := d.db.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("pool size %d, want 8", got)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				m := &Msg{Chat: testChat, ID: string(rune('a'+w)) + string(rune('0'+i%10)) + strings.Repeat("x", i/10+1),
					Sender: testChat, SenderName: "Ana", TS: int64(1000 + i), Text: "hi", Kind: "text",
					rawChat: testChat, rawSender: testChat}
				if _, err := insertMessage(d.ctx, d.db, m, true); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
}

// The whole way a client marks a chat unread: a line of JSON in, the chat list
// back out with the mark on it.
func TestUnreadOverTheWire(t *testing.T) {
	d := testDaemon(t)
	d.opts.chatLimit = 50
	if err := ensureChat(d.ctx, d.db, testChat, false, "Ana"); err != nil {
		t.Fatal(err)
	}
	storeMsg(t, d, "A1", false, "hi")
	refreshPreview(d.ctx, d.db, testChat)

	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	d.srv = &server{d: d, ln: ln, conns: map[*conn]struct{}{}}
	go d.srv.serve()
	go d.chatLoop()

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte(`{"cmd":"unread","chat":"` + testChat + `","on":true}` + "\n")); err != nil {
		t.Fatal(err)
	}

	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(client)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("no chat list came back: %v", err)
		}
		var event struct {
			Type  string `json:"type"`
			Chats []struct {
				JID          string `json:"jid"`
				Unread       int    `json:"unread"`
				ManualUnread bool   `json:"manualUnread"`
			} `json:"chats"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Type != "chats" {
			continue
		}
		for _, c := range event.Chats {
			if c.JID != testChat {
				continue
			}
			if !c.ManualUnread || c.Unread != 1 {
				t.Fatalf("the chat came back as %s", line)
			}
			return
		}
		t.Fatalf("%s is not in the list: %s", testChat, line)
	}
}
