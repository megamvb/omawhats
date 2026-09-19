package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Chat and Msg are exactly what goes over the socket; the QML side reads these
// field names.
type Chat struct {
	JID           string `json:"jid"`
	Name          string `json:"name"`
	Group         bool   `json:"group"`
	TS            int64  `json:"ts"`
	Preview       string `json:"preview"`
	PreviewFromMe bool   `json:"previewFromMe"`
	PreviewSender string `json:"previewSender"`
	Unread        int    `json:"unread"`
}

type Msg struct {
	Chat       string       `json:"chat"`
	ID         string       `json:"id"`
	Sender     string       `json:"sender"`
	SenderName string       `json:"senderName"`
	FromMe     bool         `json:"fromMe"`
	TS         int64        `json:"ts"`
	Text       string       `json:"text"`
	Kind       string       `json:"kind"`
	Status     string       `json:"status"`
	Edited     bool         `json:"edited"`
	Media      *Media       `json:"media,omitempty"`
	Link       *LinkPreview `json:"link,omitempty"`
	Quote      *Quote       `json:"quote,omitempty"`
	Reactions  []Reaction   `json:"reactions,omitempty"`

	mediaRaw []byte // the attachment as a protobuf, for downloading later

	// Where the message really came from, which read receipts must be sent back
	// to. Chat/Sender above are normalised to phone numbers where known.
	rawChat   string
	rawSender string
}

// Quote is the message a reply answers, as the reply carries it (the original
// may be long gone from this computer).
type Quote struct {
	ID     string `json:"id"`
	Sender string `json:"sender,omitempty"`
	Name   string `json:"name,omitempty"`
	FromMe bool   `json:"fromMe,omitempty"`
	Text   string `json:"text"`
	Kind   string `json:"kind,omitempty"`
	Thumb  string `json:"thumb,omitempty"`
}

// Reaction is one person's emoji on a message; a person has at most one.
type Reaction struct {
	Emoji  string `json:"emoji"`
	Sender string `json:"sender"`
	Name   string `json:"name"`
	FromMe bool   `json:"fromMe,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS owa_chat (
	jid             TEXT PRIMARY KEY,
	name            TEXT NOT NULL DEFAULT '',
	is_group        INTEGER NOT NULL DEFAULT 0,
	last_ts         INTEGER NOT NULL DEFAULT 0,
	preview         TEXT NOT NULL DEFAULT '',
	preview_from_me INTEGER NOT NULL DEFAULT 0,
	preview_sender  TEXT NOT NULL DEFAULT '',
	unread          INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS owa_message (
	chat        TEXT NOT NULL,
	id          TEXT NOT NULL,
	raw_chat    TEXT NOT NULL,
	sender      TEXT NOT NULL,
	raw_sender  TEXT NOT NULL,
	sender_name TEXT NOT NULL DEFAULT '',
	from_me     INTEGER NOT NULL,
	ts          INTEGER NOT NULL,
	text        TEXT NOT NULL,
	kind        TEXT NOT NULL DEFAULT 'text',
	status      TEXT NOT NULL DEFAULT '',
	edited      INTEGER NOT NULL DEFAULT 0,
	read        INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (chat, id)
);
CREATE INDEX IF NOT EXISTS owa_message_chat_ts ON owa_message(chat, ts);
CREATE INDEX IF NOT EXISTS owa_message_id ON owa_message(id);
-- One row per person and message. A removed reaction stays as an empty emoji
-- with its time, so an older copy arriving later cannot bring it back.
CREATE TABLE IF NOT EXISTS owa_reaction (
	chat        TEXT NOT NULL,
	msg_id      TEXT NOT NULL,
	sender      TEXT NOT NULL,
	sender_name TEXT NOT NULL DEFAULT '',
	from_me     INTEGER NOT NULL DEFAULT 0,
	emoji       TEXT NOT NULL,
	ts          INTEGER NOT NULL,
	PRIMARY KEY (chat, msg_id, sender)
);
CREATE TABLE IF NOT EXISTS owa_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

func openDB(readOnly bool) (*sql.DB, error) {
	path := dbPath()
	if readOnly {
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		// query_only rather than mode=ro: a read-only open cannot create the
		// WAL's -shm file, so it fails on a database last closed uncleanly.
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// SQLite takes one writer at a time, so a large pool only adds waiting (and
	// a file handle each). A few connections leave room for the reads whatsmeow
	// does through this same database while something is being written.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if readOnly && !schemaCurrent(db) {
		// Last written by an older daemon (the plugin reads through here while
		// the daemon is off): bring the tables up to date once, then read.
		db.Close()
		return openDB(false)
	}
	if !readOnly {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("create tables: %w", err)
		}
		if err := migrate(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
		if err := translateLabels(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("translate labels: %w", err)
		}
		os.Chmod(path, 0o600)
	}
	return db, nil
}

// schemaCurrent reports whether the newest column and table exist.
func schemaCurrent(db *sql.DB) bool {
	var cols, tables int
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('owa_message') WHERE name = 'quote'`).Scan(&cols)
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'owa_reaction'`).Scan(&tables)
	return cols == 1 && tables == 1
}

// migrate adds columns introduced after a database was first created.
func migrate(db *sql.DB) error {
	have := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('owa_message')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		rows.Scan(&name)
		have[name] = true
	}
	rows.Close()
	for _, col := range []struct{ name, def string }{
		{"media", "TEXT NOT NULL DEFAULT ''"},
		{"media_proto", "BLOB"},
		{"link", "TEXT NOT NULL DEFAULT ''"},
		{"quote", "TEXT NOT NULL DEFAULT ''"},
	} {
		if !have[col.name] {
			if _, err := db.Exec(`ALTER TABLE owa_message ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
				return err
			}
		}
	}
	return nil
}

// Versions before 0.3 stored their placeholder labels in Portuguese. The
// labels are part of the stored text (the chat list shows them as they are),
// so they are rewritten once, in place.
var labelRenames = [][2]string{
	{"📷 Foto", "📷 Photo"},
	{"🎥 Recado de vídeo", "🎥 Video note"},
	{"🎥 Vídeo", "🎥 Video"},
	{"🎤 Mensagem de voz", "🎤 Voice message"},
	{"🎵 Áudio", "🎵 Audio"},
	{"📄 Documento", "📄 Document"},
	{"🏷 Figurinha", "🏷 Sticker"},
	{"📍 Localização em tempo real", "📍 Live location"},
	{"📍 Localização", "📍 Location"},
	{"🚫 Mensagem apagada", "🚫 This message was deleted"},
}

func translateLabels(db *sql.DB) error {
	var done string
	db.QueryRow(`SELECT value FROM owa_meta WHERE key = 'labels'`).Scan(&done)
	if done == "en" {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range labelRenames {
		from, to := r[0], r[1]
		// A whole label, or a label followed by " · caption" or " (0:12)";
		// "📄 Documento" alone must not catch a file really named "Documentos".
		for _, t := range []struct{ table, col string }{{"owa_message", "text"}, {"owa_chat", "preview"}} {
			q := `UPDATE ` + t.table + ` SET ` + t.col + ` = ?2 || substr(` + t.col + `, length(?1) + 1)
				WHERE ` + t.col + ` = ?1 OR substr(` + t.col + `, 1, length(?1) + 2) IN (?1 || ' ·', ?1 || ' (')`
			if _, err := tx.Exec(q, from, to); err != nil {
				return err
			}
		}
	}
	stmts := []string{
		`UPDATE owa_message SET text = replace(text, ' contatos', ' contacts') WHERE text LIKE '👥 % contatos'`,
		`UPDATE owa_chat SET preview = replace(preview, ' contatos', ' contacts') WHERE preview LIKE '👥 % contatos'`,
		`UPDATE owa_message SET sender_name = 'You' WHERE from_me = 1 AND sender_name = 'Você'`,
		`UPDATE owa_chat SET preview_sender = 'You' WHERE preview_sender = 'Você'`,
		`INSERT INTO owa_meta (key, value) VALUES ('labels', 'en') ON CONFLICT(key) DO UPDATE SET value = 'en'`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func jsonOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case *Media:
		if x == nil {
			return ""
		}
	case *LinkPreview:
		if x == nil {
			return ""
		}
	case *Quote:
		if x == nil {
			return ""
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func (m *Msg) decodeExtras(media, link, quote string) {
	if media != "" {
		var md Media
		if json.Unmarshal([]byte(media), &md) == nil {
			m.Media = &md
		}
	}
	if link != "" {
		var lp LinkPreview
		if json.Unmarshal([]byte(link), &lp) == nil {
			m.Link = &lp
		}
	}
	if quote != "" {
		var q Quote
		if json.Unmarshal([]byte(quote), &q) == nil {
			m.Quote = &q
		}
	}
}

// execer is the database or a transaction: the statements that write take
// either, so a batch of them can share one commit.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ensureChat creates the row if needed and fills a missing name, never
// overwriting a known one with an empty one.
func ensureChat(ctx context.Context, db execer, jid string, group bool, name string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO owa_chat (jid, name, is_group) VALUES (?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE owa_chat.name END`,
		jid, name, b2i(group))
	return err
}

func setChatName(ctx context.Context, db *sql.DB, jid, name string) {
	if name == "" {
		return
	}
	db.ExecContext(ctx, `UPDATE owa_chat SET name = ? WHERE jid = ? AND name <> ?`, name, jid, name)
}

// insertMessage returns false when the message was already stored (history
// sync and live delivery overlap).
func insertMessage(ctx context.Context, db execer, m *Msg, read bool) (bool, error) {
	media, link, quote := jsonOrEmpty(m.Media), jsonOrEmpty(m.Link), jsonOrEmpty(m.Quote)
	var raw any
	if len(m.mediaRaw) > 0 {
		raw = m.mediaRaw
	}
	res, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO owa_message
			(chat, id, raw_chat, sender, raw_sender, sender_name, from_me, ts, text, kind, status, edited, read, media, media_proto, link, quote)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Chat, m.ID, m.rawChat, m.Sender, m.rawSender, m.SenderName, b2i(m.FromMe), m.TS, m.Text, m.Kind, m.Status, b2i(m.Edited), b2i(read),
		media, raw, link, quote)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 && (raw != nil || link != "" || quote != "") {
		// Seen before without its attachment (an older version stored text
		// only, or a history chunk lacked it): fill it in, never overwrite.
		db.ExecContext(ctx, `
			UPDATE owa_message SET
				media = CASE WHEN media = '' THEN ? ELSE media END,
				media_proto = COALESCE(media_proto, ?),
				link = CASE WHEN link = '' THEN ? ELSE link END,
				quote = CASE WHEN quote = '' THEN ? ELSE quote END
			WHERE chat = ? AND id = ?`, media, raw, link, quote, m.Chat, m.ID)
	}
	return n > 0, nil
}

// refreshPreview points the chat-list line at the newest stored message.
func refreshPreview(ctx context.Context, db execer, chat string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE owa_chat SET
			last_ts         = COALESCE((SELECT ts          FROM owa_message WHERE chat = ?1 ORDER BY ts DESC, rowid DESC LIMIT 1), last_ts),
			preview         = COALESCE((SELECT text        FROM owa_message WHERE chat = ?1 ORDER BY ts DESC, rowid DESC LIMIT 1), preview),
			preview_from_me = COALESCE((SELECT from_me     FROM owa_message WHERE chat = ?1 ORDER BY ts DESC, rowid DESC LIMIT 1), preview_from_me),
			preview_sender  = COALESCE((SELECT sender_name FROM owa_message WHERE chat = ?1 ORDER BY ts DESC, rowid DESC LIMIT 1), preview_sender)
		WHERE jid = ?1`, chat)
	return err
}

func bumpUnread(ctx context.Context, db *sql.DB, chat string) {
	db.ExecContext(ctx, `UPDATE owa_chat SET unread = unread + 1 WHERE jid = ?`, chat)
}

func setUnread(ctx context.Context, db execer, chat string, n int) {
	db.ExecContext(ctx, `UPDATE owa_chat SET unread = ? WHERE jid = ?`, n, chat)
}

// markNewestUnread flags the newest n incoming messages of a chat as unread, so
// that opening it later sends read receipts for exactly those.
func markNewestUnread(ctx context.Context, db execer, chat string, n int) {
	if n <= 0 {
		return
	}
	db.ExecContext(ctx, `
		UPDATE owa_message SET read = 0 WHERE rowid IN (
			SELECT rowid FROM owa_message WHERE chat = ? AND from_me = 0 ORDER BY ts DESC LIMIT ?)`, chat, n)
}

const chatColumns = `jid,
	-- No contact name: fall back to the profile name they last wrote with.
	CASE WHEN name <> '' OR is_group = 1 THEN name ELSE COALESCE((
		SELECT sender_name FROM owa_message m
		WHERE m.chat = owa_chat.jid AND m.from_me = 0 AND m.sender_name <> '' AND m.sender_name NOT LIKE '+%'
		ORDER BY m.ts DESC LIMIT 1), '') END,
	is_group, last_ts, preview, preview_from_me, preview_sender, unread`

func scanChat(sc interface{ Scan(...any) error }) (Chat, error) {
	var c Chat
	var group, fromMe int
	if err := sc.Scan(&c.JID, &c.Name, &group, &c.TS, &c.Preview, &fromMe, &c.PreviewSender, &c.Unread); err != nil {
		return c, err
	}
	c.Group, c.PreviewFromMe = group == 1, fromMe == 1
	c.Preview = preview(c.Preview)
	return c, nil
}

// listChats returns the most recent chats, plus any pinned chat too old to
// make the cut — pins are ordered by the plugin, which only sees what is
// listed here.
func listChats(ctx context.Context, db *sql.DB, limit int, pins []string) ([]Chat, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+chatColumns+` FROM owa_chat WHERE last_ts > 0 ORDER BY last_ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := []Chat{}
	seen := map[string]bool{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		seen[c.JID] = true
		chats = append(chats, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, jid := range pins {
		if seen[jid] {
			continue
		}
		if c, err := scanChat(db.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM owa_chat WHERE jid = ?`, jid)); err == nil {
			seen[jid] = true
			chats = append(chats, c)
		}
	}
	return chats, nil
}

// history returns up to limit messages older than the cursor (before, beforeID)
// — the timestamp and id of the oldest message the client already has; 0 means
// start from the newest. Oldest first, the order a conversation is read in.
// The id breaks ties between messages sent in the same second.
func history(ctx context.Context, db *sql.DB, chat string, limit int, before int64, beforeID string) ([]Msg, error) {
	if before <= 0 {
		before, beforeID = 1<<62, ""
	}
	rows, err := db.QueryContext(ctx, `
		SELECT chat, id, sender, sender_name, from_me, ts, text, kind, status, edited, media, link, quote FROM (
			SELECT rowid AS r, * FROM owa_message
			WHERE chat = ?1 AND (ts < ?2 OR (ts = ?2 AND rowid < COALESCE(
				(SELECT rowid FROM owa_message WHERE chat = ?1 AND id = ?3), -1)))
			ORDER BY ts DESC, rowid DESC LIMIT ?4
		) ORDER BY ts ASC, r ASC`, chat, before, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs := []Msg{}
	for rows.Next() {
		var m Msg
		var fromMe, edited int
		var media, link, quote string
		if err := rows.Scan(&m.Chat, &m.ID, &m.Sender, &m.SenderName, &fromMe, &m.TS, &m.Text, &m.Kind, &m.Status, &edited, &media, &link, &quote); err != nil {
			return nil, err
		}
		m.FromMe, m.Edited = fromMe == 1, edited == 1
		m.decodeExtras(media, link, quote)
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return msgs, attachReactions(ctx, db, chat, msgs)
}

// oldestMessage is what an on-demand history request to the phone is anchored
// on: "send me what came before this one".
func oldestMessage(ctx context.Context, db *sql.DB, chat string) (rawChat, id string, fromMe bool, ts int64, err error) {
	var fm int
	err = db.QueryRowContext(ctx, `
		SELECT raw_chat, id, from_me, ts FROM owa_message WHERE chat = ?
		ORDER BY ts ASC, rowid ASC LIMIT 1`, chat).Scan(&rawChat, &id, &fm, &ts)
	return rawChat, id, fm == 1, ts, err
}

func getMessage(ctx context.Context, db *sql.DB, chat, id string) (*Msg, error) {
	var m Msg
	var fromMe, edited int
	var media, link, quote string
	err := db.QueryRowContext(ctx, `
		SELECT chat, id, sender, sender_name, from_me, ts, text, kind, status, edited, media, link, quote
		FROM owa_message WHERE chat = ? AND id = ?`, chat, id).
		Scan(&m.Chat, &m.ID, &m.Sender, &m.SenderName, &fromMe, &m.TS, &m.Text, &m.Kind, &m.Status, &edited, &media, &link, &quote)
	if err != nil {
		return nil, err
	}
	m.FromMe, m.Edited = fromMe == 1, edited == 1
	m.decodeExtras(media, link, quote)
	msgs := []Msg{m}
	if err := attachReactions(ctx, db, chat, msgs); err != nil {
		return nil, err
	}
	return &msgs[0], nil
}

// attachReactions fills in the reactions of a page of messages of one chat.
func attachReactions(ctx context.Context, db *sql.DB, chat string, msgs []Msg) error {
	if len(msgs) == 0 {
		return nil
	}
	index := make(map[string]int, len(msgs))
	args := []any{chat}
	marks := make([]string, 0, len(msgs))
	for i, m := range msgs {
		index[m.ID] = i
		args = append(args, m.ID)
		marks = append(marks, "?")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT msg_id, sender, sender_name, from_me, emoji FROM owa_reaction
		WHERE chat = ? AND emoji <> '' AND msg_id IN (`+strings.Join(marks, ",")+`)
		ORDER BY ts`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r Reaction
		var fromMe int
		if err := rows.Scan(&id, &r.Sender, &r.Name, &fromMe, &r.Emoji); err != nil {
			return err
		}
		r.FromMe = fromMe == 1
		if i, ok := index[id]; ok {
			msgs[i].Reactions = append(msgs[i].Reactions, r)
		}
	}
	return rows.Err()
}

// saveReaction records a person's reaction (an empty emoji removes it),
// unless a newer one of theirs is already stored. tsMs is in milliseconds.
func saveReaction(ctx context.Context, db *sql.DB, chat, msgID string, r Reaction, tsMs int64) (bool, error) {
	res, err := db.ExecContext(ctx, `
		INSERT INTO owa_reaction (chat, msg_id, sender, sender_name, from_me, emoji, ts) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat, msg_id, sender) DO UPDATE SET
			emoji = excluded.emoji, ts = excluded.ts,
			sender_name = CASE WHEN excluded.sender_name <> '' THEN excluded.sender_name ELSE owa_reaction.sender_name END
		WHERE excluded.ts >= owa_reaction.ts`,
		chat, msgID, r.Sender, r.Name, b2i(r.FromMe), r.Emoji, tsMs)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// quoteSource is what a reply needs to know about the message it answers.
type quoteSource struct {
	rawChat, rawSender, text, kind string
	fromMe                         bool
	mediaProto                     []byte
}

func loadQuoteSource(ctx context.Context, db *sql.DB, chat, id string) (*quoteSource, error) {
	var q quoteSource
	var fromMe int
	err := db.QueryRowContext(ctx, `
		SELECT raw_chat, raw_sender, text, kind, from_me, media_proto FROM owa_message WHERE chat = ? AND id = ?`, chat, id).
		Scan(&q.rawChat, &q.rawSender, &q.text, &q.kind, &fromMe, &q.mediaProto)
	q.fromMe = fromMe == 1
	return &q, err
}

// unreadForReceipts groups a chat's unread incoming messages by the address
// they arrived on, which is what MarkRead needs.
type receiptBatch struct {
	rawChat, rawSender string
	ids                []string
}

func unreadForReceipts(ctx context.Context, db *sql.DB, chat string) ([]receiptBatch, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT raw_chat, raw_sender, id FROM owa_message
		WHERE chat = ? AND from_me = 0 AND read = 0 ORDER BY ts`, chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	index := map[[2]string]int{}
	var out []receiptBatch
	for rows.Next() {
		var rc, rs, id string
		if err := rows.Scan(&rc, &rs, &id); err != nil {
			return nil, err
		}
		k := [2]string{rc, rs}
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, receiptBatch{rawChat: rc, rawSender: rs})
		}
		out[i].ids = append(out[i].ids, id)
	}
	return out, rows.Err()
}

// markRead flags a batch of messages as read in as few statements as it can:
// one commit per id costs about ten times as much. Long batches are cut up,
// since SQLite only takes so many values in one statement.
func markRead(ctx context.Context, db execer, chat string, ids []string) {
	const perStatement = 400
	for len(ids) > 0 {
		n := min(len(ids), perStatement)
		marks := make([]string, n)
		args := make([]any, 0, n+1)
		args = append(args, chat)
		for i, id := range ids[:n] {
			marks[i] = "?"
			args = append(args, id)
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE owa_message SET read = 1 WHERE chat = ? AND id IN (`+strings.Join(marks, ",")+`)`, args...); err != nil {
			log.Printf("mark read %s: %v", chat, err)
			return
		}
		ids = ids[n:]
	}
}

func markAllRead(ctx context.Context, db *sql.DB, chat string) {
	db.ExecContext(ctx, `UPDATE owa_message SET read = 1 WHERE chat = ? AND read = 0`, chat)
}

// devicePaired reads whatsmeow's own table without going through whatsmeow, so
// the offline snapshot never has to open a writable store.
func devicePaired(ctx context.Context, db *sql.DB) (jid, pushName string, ok bool) {
	err := db.QueryRowContext(ctx, `SELECT jid, push_name FROM whatsmeow_device LIMIT 1`).Scan(&jid, &pushName)
	return jid, pushName, err == nil
}

// wipeMessages forgets every stored chat and message. whatsmeow's own tables
// (keys, contacts) are cleared by the logout itself.
func wipeMessages(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{`DELETE FROM owa_message`, `DELETE FROM owa_chat`, `DELETE FROM owa_reaction`} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	db.ExecContext(ctx, `VACUUM`)
	return nil
}
