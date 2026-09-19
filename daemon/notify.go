package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// notifyArgs is how notify-send is called. The name and the message come from
// whoever wrote them, and notify-send reads options anywhere on the line, so
// one starting with "-" would be taken as an option ("Unknown option --x", and
// no notification). Everything after "--" is text.
func notifyArgs(chat, title, body string) []string {
	return []string{
		"--app-name=OmaWhats",
		"--action=open=Open",
		"--hint=string:x-canonical-private-synchronous:owa-" + chat,
		"--", title, preview(body),
	}
}

// notify shows a desktop notification with an "Open" action. notify-send
// blocks until the notification is closed, which is how the action comes back;
// the timeout bounds that wait.
func notify(chat, title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "notify-send", notifyArgs(chat, title, body)...).Output()
	if err != nil || strings.TrimSpace(string(out)) != "open" {
		return
	}
	payload, _ := json.Marshal(map[string]string{"chat": chat})
	exec.Command("omarchy-shell", "shell", "summon", "megamvb.omawhats", string(payload)).Run()
}
