// omawhatsd — a small WhatsApp client daemon for the Omarchy shell.
//
// It is not a service: it runs when asked (`start`, or the plugin's power
// button) and exits when asked (`stop`). While running it holds the whatsmeow
// session and serves JSON lines on a unix socket; while stopped, `snapshot`
// and `history` read the local copy so old conversations stay readable.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

const usage = `usage: omawhatsd <command>

  start [options]    start the daemon in the background (returns once the socket is open)
  stop               ask the daemon to quit
  status             print the current state (JSON); exits 3 when stopped
  run [options]      run in the foreground (what start runs)
  snapshot           list the stored chats without connecting (JSON)
  history <jid> [n] [before-ts] [before-id]
                     print up to n stored messages of a chat, older than the cursor (JSON)
  logout [--wipe]    unlink this computer from WhatsApp; --wipe also deletes the
                     stored chats, messages, downloaded media and pins

options for start/run:
  --no-notify         no desktop notifications for new messages
  --no-read-receipts  do not send read receipts (blue ticks)
  --chats N           how many chats to list (default 200)
`

func main() {
	log.SetFlags(log.LstdFlags)
	cmd := "help"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	args := os.Args[min(2, len(os.Args)):]
	var err error
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "start":
		err = cmdStart(args)
	case "stop":
		err = cmdStop()
	case "status":
		cmdStatus()
	case "snapshot":
		err = cmdSnapshot()
	case "history":
		err = cmdHistory(args)
	case "logout":
		err = cmdLogout(args)
	default:
		fmt.Print(usage)
		if cmd != "help" && cmd != "-h" && cmd != "--help" {
			os.Exit(2)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "omawhatsd:", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	noNotify := fs.Bool("no-notify", false, "")
	noReceipts := fs.Bool("no-read-receipts", false, "")
	chats := fs.Int("chats", 200, "")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	return options{notify: !*noNotify, readReceipts: !*noReceipts, chatLimit: max(10, min(*chats, 2000))}, nil
}

func dial() (net.Conn, error) {
	return net.DialTimeout("unix", socketPath(), time.Second)
}

func running() bool {
	c, err := dial()
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func cmdRun(args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	// The lock, not the socket, decides who runs: a socket file can outlive a
	// crashed daemon, a flock cannot.
	lock, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("already running")
	}

	db, err := openDB(false)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d, err := newDaemon(ctx, cancel, db, opts)
	if err != nil {
		return err
	}

	sock := socketPath()
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o600)
	d.srv = &server{d: d, ln: ln, conns: map[*conn]struct{}{}}
	go d.srv.serve()
	go d.chatLoop()
	go d.outboxLoop()
	go d.cacheLoop()
	log.Printf("listening on %s", sock)
	os.WriteFile(markerPath(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	defer os.Remove(markerPath())

	if err := d.startClient(); err != nil {
		d.setState(func(s *State) { s.State, s.Error = "error", err.Error() })
	}

	<-ctx.Done()
	log.Printf("exiting")
	os.Remove(markerPath())
	if cli := d.client(); cli != nil {
		cli.Disconnect()
	}
	d.srv.broadcast(map[string]any{"type": "bye"})
	ln.Close()
	os.Remove(sock)
	return nil
}

func cmdStart(args []string) error {
	if _, err := parseOptions(args); err != nil {
		return err
	}
	if running() {
		fmt.Println("already running")
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	os.Rename(logPath(), logPath()+".old")
	logf, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, append([]string{"run"}, args...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Dir = filepath.Dir(dataDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("the daemon exited right after starting (%v) — see %s", err, logPath())
		case <-deadline:
			return fmt.Errorf("the socket did not open within 10s — see %s", logPath())
		case <-time.After(100 * time.Millisecond):
			if running() {
				cmd.Process.Release()
				fmt.Println("started")
				return nil
			}
		}
	}
}

func cmdStop() error {
	c, err := dial()
	if err != nil {
		fmt.Println("not running")
		return nil
	}
	defer c.Close()
	if _, err := c.Write([]byte(`{"cmd":"quit"}` + "\n")); err != nil {
		return err
	}
	// The daemon closes every connection on the way out; EOF means it is gone.
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 4096)
	for {
		if _, err := c.Read(buf); err != nil {
			break
		}
	}
	for i := 0; i < 50 && running(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("stopped")
	return nil
}

func printJSON(v any) {
	data, _ := json.Marshal(v)
	fmt.Println(string(data))
}

func stoppedState() State {
	s := State{Type: "state", State: "stopped", QR: []string{}}
	if db, err := openDB(true); err == nil {
		defer db.Close()
		if jid, name, ok := devicePaired(context.Background(), db); ok {
			s.Paired, s.MeName = true, name
			if at := indexByte(jid, '@'); at > 0 {
				s.Me = jid[:at]
			}
			if colon := indexByte(s.Me, ':'); colon > 0 {
				s.Me = s.Me[:colon]
			}
			if dot := indexByte(s.Me, '.'); dot > 0 {
				s.Me = s.Me[:dot]
			}
		}
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func cmdStatus() {
	c, err := dial()
	if err != nil {
		printJSON(stoppedState())
		os.Exit(3)
	}
	defer c.Close()
	c.Write([]byte(`{"cmd":"hello"}` + "\n"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		printJSON(stoppedState())
		os.Exit(3)
	}
	os.Stdout.Write(line)
}

// cmdSnapshot prints the same two lines a live client gets on hello, so the
// plugin parses one format whether the daemon is up or not.
func cmdSnapshot() error {
	printJSON(stoppedState())
	db, err := openDB(true)
	if err != nil {
		printJSON(map[string]any{"type": "chats", "chats": []Chat{}})
		return nil
	}
	defer db.Close()
	chats, err := listChats(context.Background(), db, 200, readPins())
	if err != nil {
		chats = []Chat{}
	}
	printJSON(map[string]any{"type": "chats", "chats": chats})
	return nil
}

func cmdHistory(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: history <jid> [n] [before-ts] [before-id]")
	}
	limit := 80
	if len(args) > 1 {
		if n, err := strconv.Atoi(args[1]); err == nil && n > 0 {
			limit = min(n, 500)
		}
	}
	var before int64
	var beforeID string
	if len(args) > 2 {
		before, _ = strconv.ParseInt(args[2], 10, 64)
	}
	if len(args) > 3 {
		beforeID = args[3]
	}
	msgs := []Msg{}
	if db, err := openDB(true); err == nil {
		defer db.Close()
		if m, err := history(context.Background(), db, args[0], limit, before, beforeID); err == nil {
			msgs = m
		}
	}
	// Offline there is no phone to ask, so a short page is the end of what
	// can be shown until the daemon runs again.
	more := len(msgs) == limit
	printJSON(map[string]any{"type": "history", "chat": args[0], "before": before, "messages": msgs,
		"more": more, "asked": false, "end": false, "offline": true})
	return nil
}

// cmdLogout unlinks the device. With the daemon running it is asked to do it
// (so every client sees the change); otherwise a short-lived client connects
// just long enough to log out.
func cmdLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	wipe := fs.Bool("wipe", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c, err := dial(); err == nil {
		defer c.Close()
		req, _ := json.Marshal(map[string]any{"cmd": "logout", "wipe": *wipe, "req": "cli"})
		if _, err := c.Write(append(req, '\n')); err != nil {
			return err
		}
		c.SetReadDeadline(time.Now().Add(40 * time.Second))
		rd := bufio.NewReader(c)
		for {
			line, err := rd.ReadBytes('\n')
			if err != nil {
				return fmt.Errorf("no answer from the daemon: %w", err)
			}
			var reply struct {
				Type  string `json:"type"`
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if json.Unmarshal(line, &reply) != nil || reply.Type != "loggedout" {
				continue
			}
			if !reply.OK {
				return errors.New(reply.Error)
			}
			fmt.Println("logged out")
			return nil
		}
	}

	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("the daemon is starting or stopping; try again in a moment")
	}
	db, err := openDB(false)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := newDaemon(ctx, cancel, db, options{chatLimit: 200})
	if err != nil {
		return err
	}
	device, err := d.container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}
	var unlinkErr error
	if device.ID != nil {
		cli := whatsmeow.NewClient(device, d.waLog)
		connected := make(chan struct{}, 1)
		cli.AddEventHandler(func(evt any) {
			if _, ok := evt.(*events.Connected); ok {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
		})
		if cli.Connect() == nil {
			select {
			case <-connected:
			case <-time.After(20 * time.Second):
			}
		}
		unlinkErr = unlink(ctx, cli)
		cli.Disconnect()
	}
	if *wipe {
		wipeLocal(ctx, db)
	}
	if unlinkErr != nil {
		return unlinkErr
	}
	if device.ID == nil {
		fmt.Println("not linked")
	} else {
		fmt.Println("logged out")
	}
	return nil
}
