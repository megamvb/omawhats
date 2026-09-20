package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const appName = "omawhats"

// Kept level with the plugin's manifest version. The client shows both, so a
// daemon left over from an older install can be told at a glance.
const version = "0.6.2"

func xdgDir(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

// dataDir holds the whatsmeow session and the local message history. It is the
// only place the account's keys live, so it is created 0700.
func dataDir() string {
	if v := os.Getenv("OMAWHATS_DATA"); v != "" {
		return v
	}
	return filepath.Join(xdgDir("XDG_DATA_HOME", ".local/share"), appName)
}

func stateDir() string {
	return filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"), appName)
}

func dbPath() string   { return filepath.Join(dataDir(), "whatsapp.db") }
func lockPath() string { return filepath.Join(dataDir(), "daemon.lock") }
func logPath() string  { return filepath.Join(stateDir(), "daemon.log") }

// markerPath is a plain file created once the socket accepts connections and
// removed before it closes. The plugin watches its directory for it, which is
// how it notices a start or stop without polling the socket.
func markerPath() string { return socketPath() + ".pid" }

// socketPath must match Service.qml, which derives it the same way.
func socketPath() string {
	if v := os.Getenv("OMAWHATS_SOCKET"); v != "" {
		return v
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, appName+".sock")
	}
	return filepath.Join(dataDir(), "daemon.sock")
}

// pinsPath is the plugin's list of pinned chats, in order. The plugin owns and
// writes it; the daemon only reads it, so a pinned chat is always listed.
// The emoji picker's recently used list, also written by the plugin.
func recentEmojiPath() string { return filepath.Join(dataDir(), "recent-emoji.json") }

func pinsPath() string { return filepath.Join(dataDir(), "pins.json") }

func readPins() []string {
	data, err := os.ReadFile(pinsPath())
	if err != nil {
		return nil
	}
	var pins []string
	if json.Unmarshal(data, &pins) != nil {
		return nil
	}
	return pins
}
