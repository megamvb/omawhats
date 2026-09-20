PLUGIN_ID  := megamvb.omawhats
PLUGIN_DIR := $(HOME)/.config/omarchy/plugins/$(PLUGIN_ID)
BIN_DIR    := $(HOME)/.local/bin
BIN        := omawhatsd
APP_DIR    := $(HOME)/.local/share/applications
# The repository root is the plugin folder: `omarchy plugin add <url>` clones it
# straight into ~/.config/omarchy/plugins/<id>. These are the files the shell
# loads; daemon/ and tests/ come along unused.
PLUGIN_FILES := manifest.json Model.js Service.qml Panel.qml Chat.qml QrCode.qml README.md

.PHONY: all build test validate install install-plugin install-bin uninstall

all: build

build:
	cd daemon && go build -trimpath -o $(BIN) .

test:
	cd daemon && go vet ./... && go test ./...
	node tests/run.js

# What `omarchy plugin add` runs before it will install this folder.
validate:
	omarchy-plugin-validate .

install: test install-bin install-plugin

# Installing over a running daemon is fine: the old process keeps its inode,
# the next start uses the new binary.
install-bin: build
	install -Dm755 daemon/$(BIN) $(BIN_DIR)/$(BIN)
	install -Dm755 omawhats $(BIN_DIR)/omawhats
	mkdir -p $(APP_DIR)
	sed 's|^Exec=omawhats|Exec=$(BIN_DIR)/omawhats|' omawhats.desktop >$(APP_DIR)/omawhats.desktop

install-plugin:
	mkdir -p $(PLUGIN_DIR)
	install -m644 $(PLUGIN_FILES) $(PLUGIN_DIR)/

uninstall:
	-$(BIN_DIR)/$(BIN) stop
	rm -f $(BIN_DIR)/$(BIN) $(BIN_DIR)/omawhats $(APP_DIR)/omawhats.desktop
	rm -rf $(PLUGIN_DIR)
	@echo "The session and history stay in ~/.local/share/omawhats and media in ~/.cache/omawhats (delete them by hand if you want; or run omawhatsd logout --wipe first)."
