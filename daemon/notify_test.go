package main

import (
	"strings"
	"testing"
)

// A name or a message starting with "-" must not be read as an option.
func TestNotifyArgsSeparatesText(t *testing.T) {
	args := notifyArgs(testChat, "--urgency=critical", "--icon=/etc/passwd\nsecond line")
	dash := -1
	for i, a := range args {
		if a == "--" {
			dash = i
			break
		}
	}
	if dash < 0 {
		t.Fatalf("no \"--\" in %q", args)
	}
	if got := args[dash+1:]; len(got) != 2 {
		t.Fatalf("want title and body after \"--\", got %q", got)
	}
	if args[dash+1] != "--urgency=critical" {
		t.Errorf("title = %q", args[dash+1])
	}
	if args[dash+2] != "--icon=/etc/passwd second line" {
		t.Errorf("body = %q", args[dash+2])
	}
	// Nothing before the separator may come from the message itself.
	for _, a := range args[:dash] {
		if !strings.HasPrefix(a, "--app-name=") && !strings.HasPrefix(a, "--action=") && !strings.HasPrefix(a, "--hint=") {
			t.Errorf("unexpected option %q before \"--\"", a)
		}
	}
}

func TestNotifyArgsShortensLongBody(t *testing.T) {
	args := notifyArgs(testChat, "Ana", strings.Repeat("x", 400))
	body := args[len(args)-1]
	if len([]rune(body)) != 121 || !strings.HasSuffix(body, "…") {
		t.Errorf("body not capped: %d runes", len([]rune(body)))
	}
}
