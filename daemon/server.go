package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"
)

// Request is one line from a client. Only the fields a command uses are set.
type Request struct {
	Cmd      string `json:"cmd"`
	Chat     string `json:"chat"`
	Text     string `json:"text"`
	Req      string `json:"req"`
	Limit    int    `json:"limit"`
	Before   int64  `json:"before"`
	BeforeID string `json:"beforeId"`
	ID       string `json:"id"`
	Open     bool   `json:"open"`
	On       bool   `json:"on"`    // unread: mark it, or take the mark back
	Force    bool   `json:"force"` // media: the copy here is no good, fetch another
	Wipe     bool   `json:"wipe"`
	Quote    string `json:"quote"` // send: the id of the message this answers
	Emoji    string `json:"emoji"` // react: "" removes the reaction
	// sendfile: an absolute path on this computer; Text is the caption.
	Path       string `json:"path"`
	AsDocument bool   `json:"asDocument"` // a picture sent as a file, uncompressed
	// NoAsk: page the local copy only. Sent when reading what the phone just
	// delivered, so one scroll never turns into a chain of requests.
	NoAsk bool `json:"noAsk"`
}

type conn struct {
	nc    net.Conn
	wmu   sync.Mutex
	focus string // guarded by server.mu
}

func (c *conn) write(line []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	// A client that stops reading must not stall the whole daemon.
	c.nc.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.nc.Write(line); err != nil {
		c.nc.Close()
	}
}

func (c *conn) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("encode: %v", err)
		return
	}
	c.write(append(data, '\n'))
}

type server struct {
	d     *Daemon
	ln    net.Listener
	mu    sync.Mutex
	conns map[*conn]struct{}
}

func (s *server) serve() {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		c := &conn{nc: nc}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *server) handle(c *conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.nc.Close()
	}()
	sc := bufio.NewScanner(c.nc)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var r Request
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			c.send(map[string]any{"type": "error", "error": "invalid request"})
			continue
		}
		s.d.dispatch(c, r)
	}
}

func (s *server) broadcast(v any) {
	if s == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	data = append(data, '\n')
	s.mu.Lock()
	targets := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		targets = append(targets, c)
	}
	s.mu.Unlock()
	for _, c := range targets {
		c.write(data)
	}
}

// focused reports whether some client is showing this chat right now, in
// which case new messages in it are read on arrival.
func (s *server) focused(chat string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		if c.focus == chat {
			return true
		}
	}
	return false
}

func (s *server) setFocus(c *conn, chat string) {
	s.mu.Lock()
	c.focus = chat
	s.mu.Unlock()
}

// busyError is the answer when too much is already in flight. The client shows
// it and the person can try again; it is not a state anything gets stuck in.
const busyError = "the daemon is busy — try again in a moment"

// run does a command's work off the connection's goroutine, but only so many at
// a time: a client that floods the socket must not turn every line into a
// goroutine. It reports false when there was no room.
func (d *Daemon) run(fn func()) bool {
	select {
	case d.work <- struct{}{}:
	default:
		return false
	}
	go func() {
		defer func() { <-d.work }()
		fn()
	}()
	return true
}

func (d *Daemon) dispatch(c *conn, r Request) {
	switch r.Cmd {
	case "hello":
		c.send(d.snapshotState())
		c.send(d.chatsEvent())
	case "chats":
		c.send(d.chatsEvent())
	case "history":
		limit := r.Limit
		if limit <= 0 {
			limit = 80
		}
		if limit > 500 {
			limit = 500
		}
		msgs, err := history(d.ctx, d.db, r.Chat, limit, r.Before, r.BeforeID)
		if err != nil {
			c.send(map[string]any{"type": "error", "error": err.Error()})
			return
		}
		// A short page means the local copy is exhausted. Scrolling further
		// back (a paging request, never the first page) asks the phone for
		// more; "asked" tells the client to wait for the "older" line.
		more := len(msgs) == limit
		asked, unreachable := false, false
		if !more && r.Before > 0 && !r.NoAsk {
			asked, unreachable = d.requestOlder(r.Chat)
		}
		c.send(map[string]any{"type": "history", "chat": r.Chat, "before": r.Before, "messages": msgs,
			"more": more, "asked": asked, "offline": unreachable,
			"end": !more && !asked && !unreachable && r.Before > 0 && !r.NoAsk})
	case "focus":
		d.srv.setFocus(c, r.Chat)
		if r.Chat != "" {
			go d.openChat(r.Chat)
		}
	case "unread":
		if r.Chat != "" {
			go d.setChatUnread(r.Chat, r.On)
		}
	case "send":
		ok := d.run(func() {
			m, err := d.sendText(r.Chat, r.Text, r.Quote, r.Req)
			reply := map[string]any{"type": "sent", "req": r.Req, "chat": r.Chat, "ok": err == nil}
			if err != nil {
				reply["error"] = err.Error()
			} else {
				reply["id"] = m.ID
			}
			c.send(reply)
		})
		if !ok {
			c.send(map[string]any{"type": "sent", "req": r.Req, "chat": r.Chat, "ok": false, "error": busyError})
		}
	case "sendfile":
		job := outgoing{c: c, req: r.Req, chat: r.Chat, path: r.Path, caption: r.Text, quote: r.Quote, asDocument: r.AsDocument}
		select {
		case d.outbox <- job:
		default:
			c.send(map[string]any{"type": "sent", "req": r.Req, "chat": r.Chat, "ok": false, "error": "too many files waiting to be sent"})
		}
	case "media":
		d.dl.fetch(r.Chat, r.ID, r.Force, func(md *Media, err error) {
			reply := map[string]any{"type": "media", "req": r.Req, "chat": r.Chat, "id": r.ID, "ok": err == nil, "open": r.Open}
			if err != nil {
				reply["error"] = err.Error()
			} else {
				reply["media"] = md
			}
			c.send(reply)
		})
	case "react":
		ok := d.run(func() {
			err := d.react(r.Chat, r.ID, r.Emoji)
			reply := map[string]any{"type": "reacted", "req": r.Req, "chat": r.Chat, "id": r.ID, "ok": err == nil}
			if err != nil {
				reply["error"] = err.Error()
			}
			c.send(reply)
		})
		if !ok {
			c.send(map[string]any{"type": "reacted", "req": r.Req, "chat": r.Chat, "id": r.ID, "ok": false, "error": busyError})
		}
	case "check":
		ok := d.run(func() {
			jid, name, err := d.checkNumber(r.Text)
			reply := map[string]any{"type": "check", "req": r.Req, "ok": err == nil, "jid": jid, "name": name}
			if err != nil {
				reply["error"] = err.Error()
			}
			c.send(reply)
		})
		if !ok {
			c.send(map[string]any{"type": "check", "req": r.Req, "ok": false, "jid": "", "name": "", "error": busyError})
		}
	case "pair":
		go d.repair()
	case "logout":
		go func() {
			err := d.logout(r.Wipe)
			reply := map[string]any{"type": "loggedout", "req": r.Req, "ok": err == nil, "wiped": r.Wipe}
			if err != nil {
				reply["error"] = err.Error()
			}
			c.send(reply)
		}()
	case "quit":
		go d.shutdown()
	case "ping":
		c.send(map[string]any{"type": "pong"})
	default:
		c.send(map[string]any{"type": "error", "error": "unknown command: " + r.Cmd})
	}
}
