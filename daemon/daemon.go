package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// State is broadcast whole whenever any part of it changes; the plugin never
// has to merge partial updates.
type State struct {
	Type         string   `json:"type"`
	State        string   `json:"state"` // starting|pairing|pair-timeout|connecting|connected|reconnecting|loggedout|error|stopped
	Running      bool     `json:"running"`
	Paired       bool     `json:"paired"`
	Me           string   `json:"me"`
	MeName       string   `json:"meName"`
	QR           []string `json:"qr"`
	Error        string   `json:"error"`
	ReadReceipts bool     `json:"readReceipts"`
	Syncing      bool     `json:"syncing"`
	Version      string   `json:"version"`
}

type options struct {
	notify       bool
	readReceipts bool
	chatLimit    int
}

type Daemon struct {
	ctx    context.Context
	cancel context.CancelFunc
	opts   options

	db        *sql.DB
	container *sqlstore.Container
	waLog     waLog.Logger

	mu       sync.Mutex
	cli      *whatsmeow.Client
	state    State
	qrCancel context.CancelFunc

	srv *server

	chatsDirty chan struct{}
	work       chan struct{} // how many commands may run at once
	syncTimer  *time.Timer
	dl         *downloader
	outbox     chan outgoing // files to send, one at a time

	// On-demand history: chats waiting for the phone to send older messages,
	// and chats the phone said it has nothing more for.
	olderMu      sync.Mutex
	olderPending map[string]*time.Timer
	olderDone    map[string]bool

	loggingOut bool // guarded by mu; a deliberate logout is not "unlinked by the phone"
}

func newDaemon(ctx context.Context, cancel context.CancelFunc, db *sql.DB, opts options) (*Daemon, error) {
	d := &Daemon{
		ctx:          ctx,
		cancel:       cancel,
		opts:         opts,
		db:           db,
		waLog:        waLog.Stdout("whatsmeow", "WARN", false),
		chatsDirty:   make(chan struct{}, 1),
		work:         make(chan struct{}, 32),
		outbox:       make(chan outgoing, 256),
		olderPending: map[string]*time.Timer{},
		olderDone:    map[string]bool{},
		state:        State{Type: "state", State: "starting", Running: true, QR: []string{}, ReadReceipts: opts.readReceipts, Version: version},
	}
	d.dl = newDownloader(d)
	d.container = sqlstore.NewWithDB(db, "sqlite3", waLog.Stdout("store", "WARN", false))
	if err := d.container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrade whatsmeow store: %w", err)
	}
	// What the phone lists under "Linked devices".
	store.SetOSInfo("OmaWhats", [3]uint32{1, 0, 0})
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	return d, nil
}

func (d *Daemon) client() *whatsmeow.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cli
}

func (d *Daemon) snapshotState() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.state
	s.QR = append([]string{}, d.state.QR...)
	return s
}

// setState applies fn under the lock, then broadcasts the result.
func (d *Daemon) setState(fn func(*State)) {
	d.mu.Lock()
	fn(&d.state)
	if d.state.QR == nil {
		d.state.QR = []string{}
	}
	s := d.state
	d.mu.Unlock()
	log.Printf("state: %s %s", s.State, s.Error)
	if d.srv != nil {
		d.srv.broadcast(s)
	}
}

func (d *Daemon) markChatsDirty() {
	select {
	case d.chatsDirty <- struct{}{}:
	default:
	}
}

// chatsDelay is how long a burst of changes is gathered before the chat list
// goes out again. A client rebuilds its whole list from each one, and a history
// sync touches hundreds of chats a second — while it runs the list is not what
// anyone is reading, so it is sent far less often.
func chatsDelay(syncing bool) time.Duration {
	if syncing {
		return 1500 * time.Millisecond
	}
	return 250 * time.Millisecond
}

func (d *Daemon) syncing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.Syncing
}

// chatLoop coalesces bursts into one broadcast per chatsDelay.
func (d *Daemon) chatLoop() {
	d.chatLoopWith(func() { d.srv.broadcast(d.chatsEvent()) })
}

func (d *Daemon) chatLoopWith(send func()) {
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.chatsDirty:
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(chatsDelay(d.syncing())):
			}
			send()
		}
	}
}

func (d *Daemon) chatsEvent() map[string]any {
	chats, err := listChats(d.ctx, d.db, d.opts.chatLimit, readPins())
	if err != nil {
		log.Printf("list chats: %v", err)
		chats = []Chat{}
	}
	cli := d.client()
	for i := range chats {
		if chats[i].Group || cli == nil {
			continue
		}
		// Contacts arrive by app-state sync after the chats do, so names are
		// re-resolved on every listing and written back for the offline view.
		if jid, err := types.ParseJID(chats[i].JID); err == nil {
			if name := d.contactName(cli, jid); name != "" && name != chats[i].Name {
				chats[i].Name = name
				setChatName(d.ctx, d.db, chats[i].JID, name)
			}
		}
	}
	return map[string]any{"type": "chats", "chats": chats}
}

// ---- client lifecycle

func (d *Daemon) startClient() error {
	device, err := d.container.GetFirstDevice(d.ctx)
	if err != nil {
		return fmt.Errorf("load device: %w", err)
	}
	cli := whatsmeow.NewClient(device, d.waLog)
	cli.EnableAutoReconnect = true
	cli.InitialAutoReconnect = true
	cli.AddEventHandler(d.onEvent)

	d.mu.Lock()
	d.cli = cli
	d.mu.Unlock()

	if device.ID == nil {
		qrCtx, cancel := context.WithCancel(d.ctx)
		d.mu.Lock()
		d.qrCancel = cancel
		d.mu.Unlock()
		ch, err := cli.GetQRChannel(qrCtx)
		if err != nil {
			cancel()
			return fmt.Errorf("qr channel: %w", err)
		}
		d.setState(func(s *State) { s.State, s.Paired, s.Me, s.MeName, s.Error = "connecting", false, "", "", "" })
		go d.watchQR(ch)
		return cli.Connect()
	}

	d.setState(func(s *State) {
		s.State, s.Paired, s.Error, s.QR = "connecting", true, "", nil
		s.Me = device.ID.User
		s.MeName = device.PushName
	})
	return cli.Connect()
}

func (d *Daemon) watchQR(ch <-chan whatsmeow.QRChannelItem) {
	for item := range ch {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			d.setState(func(s *State) { s.State, s.QR, s.Error = "pairing", qrRows(item.Code), "" })
		case "success":
			d.setState(func(s *State) { s.State, s.QR = "connecting", nil })
		case "timeout":
			d.setState(func(s *State) {
				s.State, s.QR, s.Error = "pair-timeout", nil, "The QR code expired. Generate a new one to try again."
			})
		case whatsmeow.QRChannelEventError:
			d.setState(func(s *State) { s.State, s.QR, s.Error = "error", nil, fmt.Sprint(item.Error) })
		default:
			if strings.HasPrefix(item.Event, "err") {
				d.setState(func(s *State) { s.State, s.QR, s.Error = "error", nil, "Pairing failed: "+item.Event })
			}
		}
	}
}

func qrRows(code string) []string {
	q, err := qrcode.New(code, qrcode.Medium)
	if err != nil {
		return nil
	}
	q.DisableBorder = true
	bm := q.Bitmap()
	rows := make([]string, len(bm))
	for y, row := range bm {
		var b strings.Builder
		for _, on := range row {
			if on {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[y] = b.String()
	}
	return rows
}

// repair throws the current client away and starts over, which is how a new
// QR is produced after a timeout or a logout.
func (d *Daemon) repair() {
	d.mu.Lock()
	cli, cancel := d.cli, d.qrCancel
	d.cli, d.qrCancel = nil, nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cli != nil {
		if cli.IsLoggedIn() {
			d.mu.Lock()
			d.cli = cli
			d.mu.Unlock()
			return
		}
		cli.RemoveEventHandlers()
		cli.Disconnect()
	}
	if err := d.startClient(); err != nil {
		d.setState(func(s *State) { s.State, s.Error = "error", err.Error() })
	}
}

// logout unlinks this computer from the account — the same as removing it
// under Linked devices on the phone. With wipe, the stored chats, messages,
// downloaded media and pins go too.
func (d *Daemon) logout(wipe bool) error {
	d.mu.Lock()
	cli, cancel := d.cli, d.qrCancel
	d.qrCancel = nil
	d.loggingOut = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.loggingOut = false
		d.mu.Unlock()
	}()
	if cancel != nil {
		cancel()
	}
	var err error
	if cli != nil && cli.Store.ID != nil {
		err = unlink(d.ctx, cli)
	}
	if wipe {
		wipeLocal(d.ctx, d.db)
	}
	d.olderMu.Lock()
	d.olderDone = map[string]bool{}
	d.olderMu.Unlock()
	d.setState(func(s *State) {
		s.State, s.Paired, s.Me, s.MeName, s.QR, s.Error, s.Syncing = "loggedout", false, "", "", nil, "", false
		if err != nil {
			s.Error = err.Error()
		}
	})
	d.markChatsDirty()
	return err
}

// unlink tells WhatsApp this device is gone. If the server cannot be reached
// the keys are deleted anyway — the account is unusable from here either way —
// and the error says the phone may still list the device.
func unlink(ctx context.Context, cli *whatsmeow.Client) error {
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := cli.Logout(lctx)
	if err == nil {
		return nil
	}
	log.Printf("logout: %v", err)
	cli.Disconnect()
	if cli.Store.ID != nil {
		cli.Store.Delete(ctx)
	}
	return errors.New("Removed from this computer, but WhatsApp could not be told. " +
		"If \"Omarchy\" still shows under Linked devices on your phone, remove it there.")
}

func wipeLocal(ctx context.Context, db *sql.DB) {
	if err := wipeMessages(ctx, db); err != nil {
		log.Printf("wipe messages: %v", err)
	}
	os.RemoveAll(cacheDir())
	os.Remove(pinsPath())
	os.Remove(recentEmojiPath())
}

func (d *Daemon) shutdown() {
	if cli := d.client(); cli != nil {
		cli.Disconnect()
	}
	d.cancel()
}

// ---- events

func (d *Daemon) onEvent(raw any) {
	switch evt := raw.(type) {
	case *events.Connected:
		cli := d.client()
		d.setState(func(s *State) {
			s.State, s.Paired, s.Error, s.QR = "connected", true, "", nil
			if cli != nil && cli.Store.ID != nil {
				s.Me, s.MeName = cli.Store.ID.User, cli.Store.PushName
			}
		})
		go d.fillGroupNames()
		d.markChatsDirty()
	case *events.PairSuccess:
		d.setState(func(s *State) { s.State, s.Paired, s.Me, s.QR = "connecting", true, evt.ID.User, nil })
	case *events.Disconnected:
		if d.ctx.Err() == nil {
			d.setState(func(s *State) {
				if s.State != "loggedout" && s.State != "pairing" && s.State != "pair-timeout" {
					s.State = "reconnecting"
				}
			})
		}
	case *events.KeepAliveTimeout:
		d.setState(func(s *State) { s.State = "reconnecting" })
	case *events.KeepAliveRestored:
		d.setState(func(s *State) { s.State = "connected" })
	case *events.LoggedOut:
		d.mu.Lock()
		deliberate := d.loggingOut
		d.mu.Unlock()
		if deliberate {
			return
		}
		d.setState(func(s *State) {
			s.State, s.Paired, s.Me, s.MeName, s.QR = "loggedout", false, "", "", nil
			s.Error = "This computer was unlinked from the phone."
		})
	case *events.StreamReplaced:
		d.setState(func(s *State) { s.State, s.Error = "error", "Another instance took over this session." })
	case *events.TemporaryBan:
		d.setState(func(s *State) { s.State, s.Error = "error", "Temporary ban: "+evt.String() })
	case *events.ClientOutdated:
		d.setState(func(s *State) {
			s.State, s.Error = "error", "whatsmeow is outdated — rebuild the daemon."
		})
	case *events.ConnectFailure:
		d.setState(func(s *State) { s.State, s.Error = "error", fmt.Sprintf("Connection failed: %s", evt.Reason) })
	case *events.PushNameSetting:
		d.setState(func(s *State) { s.MeName = evt.Action.GetName() })
	case *events.Message:
		d.handleMessage(evt, true)
	case *events.HistorySync:
		d.handleHistory(evt)
	case *events.Receipt:
		d.handleReceipt(evt)
	case *events.MediaRetry:
		d.dl.handleRetry(evt)
	case *events.GroupInfo:
		if evt.Name != nil {
			setChatName(d.ctx, d.db, evt.JID.String(), evt.Name.Name)
			d.markChatsDirty()
		}
	case *events.JoinedGroup:
		ensureChat(d.ctx, d.db, evt.JID.String(), true, evt.GroupInfo.Name)
		d.markChatsDirty()
	case *events.Contact, *events.PushName:
		d.markChatsDirty()
	case *events.MarkChatAsRead:
		chat := d.canonical(evt.JID, types.EmptyJID).String()
		if evt.Action.GetRead() {
			setUnread(d.ctx, d.db, chat, 0)
			markAllRead(d.ctx, d.db, chat)
		} else {
			// Marked unread on the phone or another linked device.
			markManualUnread(d.ctx, d.db, chat)
		}
		d.markChatsDirty()
	}
}

// canonical maps an address to the phone-number form when the LID store knows
// it, so one person is one chat whichever way WhatsApp addressed them.
func (d *Daemon) canonical(jid, alt types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if !alt.IsEmpty() && alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if cli := d.client(); storeReady(cli) {
		if pn, err := cli.Store.LIDs.GetPNForLID(d.ctx, jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid
}

// storeReady reports whether the device store has its per-account parts. An
// unpaired device (first run, or just logged out) has no contact or LID
// store, and touching them panics.
func storeReady(cli *whatsmeow.Client) bool {
	return cli != nil && cli.Store != nil && cli.Store.ID != nil && cli.Store.Contacts != nil && cli.Store.LIDs != nil
}

func (d *Daemon) contactName(cli *whatsmeow.Client, jid types.JID) string {
	if !storeReady(cli) {
		return ""
	}
	c, err := cli.Store.Contacts.GetContact(d.ctx, jid)
	if (err != nil || !c.Found) && jid.Server == types.DefaultUserServer {
		// Newer contacts are often stored under the LID only.
		if lid, lerr := cli.Store.LIDs.GetLIDForPN(d.ctx, jid); lerr == nil && !lid.IsEmpty() {
			c, err = cli.Store.Contacts.GetContact(d.ctx, lid)
		}
	}
	if err != nil || !c.Found {
		return ""
	}
	for _, n := range []string{c.FullName, c.FirstName, c.BusinessName, c.PushName} {
		if strings.TrimSpace(n) != "" {
			return n
		}
	}
	return ""
}

func phoneLabel(jid types.JID) string {
	if jid.Server == types.DefaultUserServer {
		return "+" + jid.User
	}
	return jid.User
}

func (d *Daemon) displayName(jid types.JID, pushName string) string {
	if cli := d.client(); cli != nil {
		if n := d.contactName(cli, jid); n != "" {
			return n
		}
	}
	if pushName != "" {
		return pushName
	}
	return phoneLabel(jid)
}

func skipChat(chat types.JID) bool {
	return chat.Server == types.BroadcastServer || chat.Server == types.NewsletterServer || chat.IsEmpty()
}

func (d *Daemon) groupName(chat types.JID) string {
	var name string
	d.db.QueryRowContext(d.ctx, `SELECT name FROM owa_chat WHERE jid = ?`, chat.String()).Scan(&name)
	if name != "" {
		return name
	}
	if cli := d.client(); cli != nil && cli.IsConnected() {
		ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
		defer cancel()
		if info, err := cli.GetGroupInfo(ctx, chat); err == nil {
			return info.Name
		}
	}
	return ""
}

func (d *Daemon) fillGroupNames() {
	cli := d.client()
	if cli == nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()
	groups, err := cli.GetJoinedGroups(ctx)
	if err != nil {
		log.Printf("joined groups: %v", err)
		return
	}
	for _, g := range groups {
		ensureChat(d.ctx, d.db, g.JID.String(), true, g.Name)
	}
	d.markChatsDirty()
}

// toMsg converts a whatsmeow message; ok is false for anything that is not a
// visible chat message.
func (d *Daemon) toMsg(evt *events.Message) (m *Msg, ok bool) {
	info := evt.Info
	if skipChat(info.Chat) {
		return nil, false
	}
	text, kind := describe(evt.Message)
	if text == "" {
		return nil, false
	}
	chat := d.chatOf(info)
	sender := d.canonical(info.Sender, info.SenderAlt)
	senderName := ""
	if info.IsFromMe {
		senderName = "You"
	} else {
		senderName = d.displayName(sender, info.PushName)
	}
	m = &Msg{
		Chat:       chat.String(),
		ID:         info.ID,
		Sender:     sender.String(),
		SenderName: senderName,
		FromMe:     info.IsFromMe,
		TS:         info.Timestamp.Unix(),
		Text:       text,
		Kind:       kind,
		Status:     map[bool]string{true: "sent", false: ""}[info.IsFromMe],
		rawChat:    info.Chat.ToNonAD().String(),
		rawSender:  info.Sender.ToNonAD().String(),
	}
	m.mediaRaw = attach(m, evt.Message)
	m.Quote = d.quoteOf(m.Chat, evt.Message)
	return m, true
}

// chatOf is the chat a message belongs to, under the address it is stored by.
func (d *Daemon) chatOf(info types.MessageInfo) types.JID {
	switch {
	case info.IsGroup:
		return info.Chat.ToNonAD()
	case info.IsFromMe:
		return d.canonical(info.Chat, info.RecipientAlt)
	default:
		return d.canonical(info.Chat, info.SenderAlt)
	}
}

// meJID is this account's phone-number address.
func (d *Daemon) meJID() types.JID {
	if cli := d.client(); cli != nil && cli.Store != nil && cli.Store.ID != nil {
		return cli.Store.ID.ToNonAD()
	}
	return types.EmptyJID
}

func (d *Daemon) isMe(jid types.JID) bool {
	cli := d.client()
	if cli == nil || cli.Store == nil || cli.Store.ID == nil || jid.IsEmpty() {
		return false
	}
	return jid.User == cli.Store.ID.User || (!cli.Store.LID.IsEmpty() && jid.User == cli.Store.LID.User)
}

// quoteOf reads the message a reply answers. The reply carries a copy of it;
// the stored original, when there is one, knows the sender's name better.
func (d *Daemon) quoteOf(chat string, m *waE2E.Message) *Quote {
	ci := contextOf(m)
	if ci == nil {
		return nil
	}
	q := &Quote{ID: ci.GetStanzaID()}
	q.Text, q.Kind = describe(ci.GetQuotedMessage())
	if _, _, thumb := mediaOf(ci.GetQuotedMessage()); len(thumb) > 0 {
		q.Thumb = writeThumb(q.ID, ".thumb.jpg", thumb)
	}
	if p, err := types.ParseJID(ci.GetParticipant()); err == nil && !p.IsEmpty() {
		if d.isMe(p) {
			q.FromMe, q.Name = true, "You"
			q.Sender = d.meJID().String()
		} else {
			sender := d.canonical(p, types.EmptyJID)
			q.Sender = sender.String()
			q.Name = d.displayName(sender, "")
		}
	}
	var name, text, kind string
	var fromMe int
	if d.db.QueryRowContext(d.ctx, `SELECT sender_name, from_me, text, kind FROM owa_message WHERE chat = ? AND id = ?`,
		chat, q.ID).Scan(&name, &fromMe, &text, &kind) == nil {
		q.FromMe = fromMe == 1
		if q.FromMe {
			q.Name = "You"
		} else if name != "" {
			q.Name = name
		}
		if q.Text == "" {
			q.Text, q.Kind = text, kind
		}
	}
	if q.Text == "" {
		q.Text = "Message"
	}
	return q
}

func (d *Daemon) handleProtocol(evt *events.Message, pm *waE2E.ProtocolMessage) {
	id := pm.GetKey().GetID()
	if id == "" {
		return
	}
	var text, kind string
	edited := 0
	switch pm.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		text, kind = "🚫 This message was deleted", "revoked"
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		text, kind = describe(pm.GetEditedMessage())
		edited = 1
	default:
		return
	}
	if text == "" {
		return
	}
	var chat string
	if d.db.QueryRowContext(d.ctx, `SELECT chat FROM owa_message WHERE id = ?`, id).Scan(&chat) != nil {
		return
	}
	d.db.ExecContext(d.ctx, `UPDATE owa_message SET text = ?, kind = ?, edited = ? WHERE chat = ? AND id = ?`, text, kind, edited, chat, id)
	refreshPreview(d.ctx, d.db, chat)
	if m, err := getMessage(d.ctx, d.db, chat, id); err == nil {
		d.srv.broadcast(map[string]any{"type": "message", "chat": chat, "message": m, "update": true})
	}
	d.markChatsDirty()
}

func (d *Daemon) handleMessage(evt *events.Message, live bool) {
	if rm := evt.Message.GetReactionMessage(); rm != nil {
		d.handleReaction(evt.Info, rm, live)
		return
	}
	if pm := protocolOf(evt.Message); pm != nil {
		if live {
			d.handleProtocol(evt, pm)
		}
		return
	}
	m, ok := d.toMsg(evt)
	if !ok {
		return
	}
	isGroup := evt.Info.IsGroup
	chatJID, _ := types.ParseJID(m.Chat)
	name := ""
	if isGroup {
		name = d.groupName(chatJID)
	} else if !m.FromMe {
		name = d.displayName(chatJID, evt.Info.PushName)
	}
	if err := ensureChat(d.ctx, d.db, m.Chat, isGroup, name); err != nil {
		log.Printf("chat %s: %v", m.Chat, err)
		return
	}
	focused := live && d.srv.focused(m.Chat)
	read := !live || m.FromMe || focused
	inserted, err := insertMessage(d.ctx, d.db, m, read)
	if err != nil {
		log.Printf("store message: %v", err)
		return
	}
	if !inserted {
		return
	}
	refreshPreview(d.ctx, d.db, m.Chat)
	if live {
		switch {
		case m.FromMe:
			// Replying from the phone means the chat was read there.
			setUnread(d.ctx, d.db, m.Chat, 0)
			markAllRead(d.ctx, d.db, m.Chat)
		case focused:
			d.sendReceipts(m.Chat)
		default:
			bumpUnread(d.ctx, d.db, m.Chat)
			if d.opts.notify {
				title := name
				if title == "" {
					title = m.SenderName
				}
				body := m.Text
				if isGroup {
					body = m.SenderName + ": " + body
				}
				go notify(m.Chat, title, body)
			}
		}
		d.srv.broadcast(map[string]any{"type": "message", "chat": m.Chat, "message": m})
	}
	d.markChatsDirty()
}

// storeMessages keeps a batch of messages in one transaction and refreshes the
// chat's preview line; it returns how many were new. A commit for each of
// thousands of history messages costs about ten times as much.
func (d *Daemon) storeMessages(chat string, msgs []*Msg) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	tx, err := d.db.BeginTx(d.ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	inserted := 0
	for _, m := range msgs {
		ok, err := insertMessage(d.ctx, tx, m, true)
		if err != nil {
			return 0, err
		}
		if ok {
			inserted++
		}
	}
	if err := refreshPreview(d.ctx, tx, chat); err != nil {
		return 0, err
	}
	return inserted, tx.Commit()
}

func (d *Daemon) handleHistory(evt *events.HistorySync) {
	cli := d.client()
	if cli == nil {
		return
	}
	onDemand := evt.Data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND
	if !onDemand {
		d.setState(func(s *State) { s.Syncing = true })
	}
	answered := 0
	for _, conv := range evt.Data.GetConversations() {
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil || skipChat(chatJID) {
			continue
		}
		isGroup := chatJID.Server == types.GroupServer
		alt := types.EmptyJID
		if pn, err := types.ParseJID(conv.GetPnJID()); err == nil {
			alt = pn
		}
		chat := d.canonical(chatJID, alt)
		name := conv.GetName()
		if name == "" {
			name = conv.GetDisplayName()
		}
		if err := ensureChat(d.ctx, d.db, chat.String(), isGroup, name); err != nil {
			continue
		}
		// The messages of one conversation are stored together; the reactions
		// wait for that transaction to be done with the write lock, and by then
		// the message each one belongs to is stored.
		batch := []*Msg{}
		var reactions []func()
		for _, hm := range conv.GetMessages() {
			parsed, err := cli.ParseWebMessage(chatJID, hm.GetMessage())
			if err != nil {
				continue
			}
			if rm := parsed.Message.GetReactionMessage(); rm != nil {
				info := parsed.Info
				reactions = append(reactions, func() { d.handleReaction(info, rm, false) })
				continue
			}
			if pm := protocolOf(parsed.Message); pm != nil {
				continue
			}
			m, ok := d.toMsg(parsed)
			if !ok {
				continue
			}
			m.Chat = chat.String()
			if m.FromMe {
				m.Status = "sent"
			}
			batch = append(batch, m)
			if list := hm.GetMessage().GetReactions(); len(list) > 0 {
				id, on := m.ID, chat
				reactions = append(reactions, func() { d.historyReactions(on.String(), id, on, list) })
			}
		}
		inserted, err := d.storeMessages(chat.String(), batch)
		if err != nil {
			log.Printf("store history for %s: %v", chat, err)
		}
		for _, apply := range reactions {
			apply()
		}
		if onDemand {
			// Older messages only: the unread count is not the phone's to reset here.
			noMore := conv.GetEndOfHistoryTransferType() == waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY
			if d.finishOlder(chat.String(), inserted, noMore, false) {
				answered++
			}
			continue
		}
		if n := int(conv.GetUnreadCount()); n >= 0 {
			setUnread(d.ctx, d.db, chat.String(), n)
			markNewestUnread(d.ctx, d.db, chat.String(), n)
		}
	}
	d.markChatsDirty()
	if onDemand {
		// An empty answer names no chat; with a single request outstanding it
		// can only be that one's, and it means there is nothing older.
		if answered == 0 {
			d.olderMu.Lock()
			var only string
			if len(d.olderPending) == 1 {
				for chat := range d.olderPending {
					only = chat
				}
			}
			d.olderMu.Unlock()
			if only != "" {
				d.finishOlder(only, 0, true, false)
			}
		}
		return
	}
	// History arrives in several chunks with no "done" marker; call it done
	// once the chunks stop.
	d.mu.Lock()
	if d.syncTimer != nil {
		d.syncTimer.Stop()
	}
	d.syncTimer = time.AfterFunc(8*time.Second, func() {
		d.setState(func(s *State) { s.Syncing = false })
		// The last listing of the sync went out on the long delay; send one
		// more now that the short one is back.
		d.markChatsDirty()
	})
	d.mu.Unlock()
}

var statusRank = map[string]int{"": 0, "sent": 1, "delivered": 2, "read": 3}

func (d *Daemon) handleReceipt(evt *events.Receipt) {
	switch evt.Type {
	case types.ReceiptTypeReadSelf:
		chat := d.canonical(evt.Chat, types.EmptyJID).String()
		setUnread(d.ctx, d.db, chat, 0)
		markAllRead(d.ctx, d.db, chat)
		d.markChatsDirty()
		return
	case types.ReceiptTypeDelivered, types.ReceiptTypeRead, types.ReceiptTypePlayed:
	default:
		return
	}
	status := "delivered"
	if evt.Type != types.ReceiptTypeDelivered {
		status = "read"
	}
	for _, id := range evt.MessageIDs {
		var chat, cur string
		if d.db.QueryRowContext(d.ctx, `SELECT chat, status FROM owa_message WHERE id = ? AND from_me = 1`, id).Scan(&chat, &cur) != nil {
			continue
		}
		if statusRank[status] <= statusRank[cur] {
			continue
		}
		d.db.ExecContext(d.ctx, `UPDATE owa_message SET status = ? WHERE chat = ? AND id = ?`, status, chat, id)
		d.srv.broadcast(map[string]any{"type": "status", "chat": chat, "id": id, "status": status})
	}
}

// ---- commands

// broadcastMessage re-sends a stored message whole, as an update; clients
// replace their copy by id.
func (d *Daemon) broadcastMessage(chat, id string) {
	if m, err := getMessage(d.ctx, d.db, chat, id); err == nil {
		d.srv.broadcast(map[string]any{"type": "message", "chat": chat, "message": m, "update": true})
	}
}

// openChat is what "focus" does: clear the badge and, if enabled and online,
// tell the sender it was read.
func (d *Daemon) openChat(chat string) {
	setUnread(d.ctx, d.db, chat, 0)
	d.sendReceipts(chat)
	d.markChatsDirty()
}

// setChatUnread is the "unread" command: the reader flags a chat to come back
// to, or takes the flag back. The phone and the other linked devices hear about
// it too, so the mark is the same everywhere.
func (d *Daemon) setChatUnread(chat string, on bool) {
	if on {
		markManualUnread(d.ctx, d.db, chat)
	} else {
		setUnread(d.ctx, d.db, chat, 0)
		d.sendReceipts(chat)
	}
	d.markChatsDirty()
	d.syncChatRead(chat, !on)
}

// syncChatRead sends the app-state patch behind "mark as read / unread". The
// mark here does not depend on it: it gives up quietly while the daemon is
// offline, or if the account has no app-state keys to sign the patch with.
func (d *Daemon) syncChatRead(chat string, read bool) {
	cli := d.client()
	if cli == nil || !cli.IsLoggedIn() || !cli.IsConnected() {
		return
	}
	target, ts := chat, time.Now()
	var key *waCommon.MessageKey
	if m, ok := newestMessage(d.ctx, d.db, chat); ok {
		target, ts = m.rawChat, time.Unix(m.ts, 0)
		key = &waCommon.MessageKey{
			RemoteJID: proto.String(m.rawChat),
			FromMe:    proto.Bool(m.fromMe),
			ID:        proto.String(m.id),
		}
		if !m.fromMe && m.rawSender != "" && m.rawSender != m.rawChat {
			key.Participant = proto.String(m.rawSender)
		}
	}
	jid, err := types.ParseJID(target)
	if err != nil {
		return
	}
	if err := cli.SendAppState(d.ctx, appstate.BuildMarkChatAsRead(jid, read, ts, key)); err != nil {
		log.Printf("mark %s read=%v: %v", chat, read, err)
	}
}

func (d *Daemon) sendReceipts(chat string) {
	cli := d.client()
	if cli == nil || !cli.IsLoggedIn() {
		return
	}
	batches, err := unreadForReceipts(d.ctx, d.db, chat)
	if err != nil || len(batches) == 0 {
		return
	}
	for _, b := range batches {
		if !d.opts.readReceipts {
			markRead(d.ctx, d.db, chat, b.ids)
			continue
		}
		rc, err1 := types.ParseJID(b.rawChat)
		rs, err2 := types.ParseJID(b.rawSender)
		if err1 != nil || err2 != nil {
			markRead(d.ctx, d.db, chat, b.ids)
			continue
		}
		if err := cli.MarkRead(d.ctx, b.ids, time.Now(), rc, rs); err != nil {
			log.Printf("mark read %s: %v", chat, err)
			continue
		}
		markRead(d.ctx, d.db, chat, b.ids)
	}
}

func (d *Daemon) sendText(chat, text, quoteID, req string) (*Msg, error) {
	text = strings.TrimRight(text, " \t\r\n")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("empty message")
	}
	cli := d.client()
	if cli == nil || !cli.IsLoggedIn() || !cli.IsConnected() {
		return nil, errors.New("not connected")
	}
	to, err := types.ParseJID(chat)
	if err != nil {
		return nil, fmt.Errorf("invalid chat: %w", err)
	}
	msg := &waE2E.Message{Conversation: proto.String(text)}
	var quote *Quote
	if quoteID != "" {
		ci, q, err := d.replyContext(chat, quoteID)
		if err != nil {
			return nil, err
		}
		msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text), ContextInfo: ci}}
		quote = q
	}
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return nil, err
	}
	m := d.sentMsg(chat, resp, text, "text", quote)
	d.storeSent(to, m, req)
	return m, nil
}

// replyContext is what makes a message a reply: the id and author of the
// message it answers, and a copy of that message for the preview the other
// side shows.
func (d *Daemon) replyContext(chat, id string) (*waE2E.ContextInfo, *Quote, error) {
	src, err := loadQuoteSource(d.ctx, d.db, chat, id)
	if err != nil {
		return nil, nil, errors.New("the message being answered is not stored here")
	}
	if src.kind == "revoked" {
		return nil, nil, errors.New("that message was deleted")
	}
	quoted := &waE2E.Message{Conversation: proto.String(src.text)}
	if len(src.mediaProto) > 0 {
		var wm waE2E.Message
		if proto.Unmarshal(src.mediaProto, &wm) == nil {
			quoted = &wm
		}
	}
	participant := src.rawSender
	if src.fromMe || participant == "" {
		participant = d.meJID().String()
	}
	ci := &waE2E.ContextInfo{
		StanzaID:      proto.String(id),
		Participant:   proto.String(participant),
		QuotedMessage: quoted,
	}
	return ci, d.quoteOf(chat, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{ContextInfo: ci}}), nil
}

// ---- reactions

// handleReaction stores someone's reaction (live or from history) and, live,
// re-sends the message it is on to every client.
func (d *Daemon) handleReaction(info types.MessageInfo, rm *waE2E.ReactionMessage, live bool) {
	id := rm.GetKey().GetID()
	if id == "" || skipChat(info.Chat) {
		return
	}
	// The message's own chat when it is stored; a reaction to a message
	// not stored yet is kept for when it arrives.
	var chat string
	var targetFromMe int
	var targetText string
	stored := d.db.QueryRowContext(d.ctx, `SELECT chat, from_me, text FROM owa_message WHERE id = ?`, id).
		Scan(&chat, &targetFromMe, &targetText) == nil
	if !stored {
		chat = d.chatOf(info).String()
	}
	r := Reaction{Emoji: rm.GetText(), FromMe: info.IsFromMe}
	if info.IsFromMe {
		r.Sender, r.Name = d.meJID().String(), "You"
	} else {
		sender := d.canonical(info.Sender, info.SenderAlt)
		r.Sender, r.Name = sender.String(), d.displayName(sender, info.PushName)
	}
	ts := rm.GetSenderTimestampMS()
	if ts <= 0 {
		ts = info.Timestamp.UnixMilli()
	}
	changed, err := saveReaction(d.ctx, d.db, chat, id, r, ts)
	if err != nil {
		log.Printf("store reaction: %v", err)
		return
	}
	if !live || !changed || !stored {
		return
	}
	d.broadcastMessage(chat, id)
	// Like WhatsApp: a reaction to one of your messages is worth a notice.
	if r.Emoji != "" && !r.FromMe && targetFromMe == 1 && d.opts.notify && !d.srv.focused(chat) {
		title := r.Name
		if info.IsGroup {
			if g := d.groupName(info.Chat.ToNonAD()); g != "" {
				title = r.Name + " · " + g
			}
		}
		go notify(chat, title, "Reacted "+r.Emoji+" to “"+preview(targetText)+"”")
	}
}

// historyReactions stores the reactions a history sync attaches to a
// message. Each names its author by its own key: from me, the group member,
// or else the other person of a one-to-one chat.
func (d *Daemon) historyReactions(chat, id string, rawChat types.JID, list []*waWeb.Reaction) {
	for _, hr := range list {
		key := hr.GetKey()
		r := Reaction{Emoji: hr.GetText(), FromMe: key.GetFromMe()}
		switch {
		case r.FromMe:
			r.Sender, r.Name = d.meJID().String(), "You"
		default:
			author := rawChat
			if p, err := types.ParseJID(key.GetParticipant()); err == nil && !p.IsEmpty() {
				author = p
			}
			if author.Server == types.GroupServer {
				continue
			}
			sender := d.canonical(author, types.EmptyJID)
			r.Sender, r.Name = sender.String(), d.displayName(sender, "")
		}
		if _, err := saveReaction(d.ctx, d.db, chat, id, r, hr.GetSenderTimestampMS()); err != nil {
			log.Printf("store reaction: %v", err)
		}
	}
}

// react sets (or, with an empty emoji, removes) this account's reaction to a
// message.
func (d *Daemon) react(chat, id, emoji string) error {
	cli := d.client()
	if cli == nil || !cli.IsLoggedIn() || !cli.IsConnected() {
		return errors.New("not connected")
	}
	src, err := loadQuoteSource(d.ctx, d.db, chat, id)
	if err != nil {
		return errors.New("that message is not stored here")
	}
	rawChat, err := types.ParseJID(src.rawChat)
	if err != nil {
		return fmt.Errorf("invalid chat: %w", err)
	}
	sender := types.EmptyJID
	if !src.fromMe {
		if sender, err = types.ParseJID(src.rawSender); err != nil {
			return fmt.Errorf("invalid sender: %w", err)
		}
	}
	msg := cli.BuildReaction(rawChat, sender, id, emoji)
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()
	if _, err := cli.SendMessage(ctx, rawChat, msg); err != nil {
		return err
	}
	r := Reaction{Emoji: emoji, FromMe: true, Sender: d.meJID().String(), Name: "You"}
	if _, err := saveReaction(d.ctx, d.db, chat, id, r, msg.GetReactionMessage().GetSenderTimestampMS()); err != nil {
		log.Printf("store reaction: %v", err)
	}
	d.broadcastMessage(chat, id)
	return nil
}

// ---- older messages, from the phone

// requestOlder asks the phone for the messages before the oldest one stored
// for chat. It returns false when there is no point asking (offline, nothing
// stored to anchor on, or the phone already said there is nothing more) —
// with unreachable set in the first case, where trying later may still work.
// The answer arrives as an ON_DEMAND history sync and is announced with an
// "older" line.
func (d *Daemon) requestOlder(chat string) (asked, unreachable bool) {
	cli := d.client()
	if cli == nil || !cli.IsLoggedIn() || !cli.IsConnected() {
		return false, true
	}
	d.olderMu.Lock()
	if d.olderDone[chat] {
		d.olderMu.Unlock()
		return false, false
	}
	if _, pending := d.olderPending[chat]; pending {
		d.olderMu.Unlock()
		return true, false
	}
	d.olderMu.Unlock()

	rawChat, id, fromMe, ts, err := oldestMessage(d.ctx, d.db, chat)
	if err != nil {
		return false, false
	}
	rc, err := types.ParseJID(rawChat)
	if err != nil {
		return false, false
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: rc, IsFromMe: fromMe, IsGroup: rc.Server == types.GroupServer},
		ID:            id,
		Timestamp:     time.Unix(ts, 0),
	}
	ctx, cancel := context.WithTimeout(d.ctx, 20*time.Second)
	defer cancel()
	if _, err := cli.SendPeerMessage(ctx, cli.BuildHistorySyncRequest(info, 50)); err != nil {
		log.Printf("older messages for %s: %v", chat, err)
		return false, false
	}
	log.Printf("asked the phone for messages before %s in %s", id, chat)
	d.olderMu.Lock()
	d.olderPending[chat] = time.AfterFunc(45*time.Second, func() { d.finishOlder(chat, 0, false, true) })
	d.olderMu.Unlock()
	return true, false
}

// finishOlder settles a request and tells every client. It reports whether a
// request was actually waiting.
func (d *Daemon) finishOlder(chat string, count int, noMore, timedOut bool) bool {
	d.olderMu.Lock()
	t, pending := d.olderPending[chat]
	if pending {
		t.Stop()
		delete(d.olderPending, chat)
	}
	end := noMore || (count == 0 && !timedOut)
	if end {
		d.olderDone[chat] = true
	}
	d.olderMu.Unlock()
	if !pending && count == 0 {
		return false
	}
	d.srv.broadcast(map[string]any{"type": "older", "chat": chat, "count": count, "end": end, "timeout": timedOut})
	return pending
}

// ---- new chats

// checkNumber resolves a phone number typed by the user to the chat WhatsApp
// knows it by (which, for some Brazilian mobiles, lacks the ninth digit).
func (d *Daemon) checkNumber(input string) (jid, name string, err error) {
	var digits strings.Builder
	for _, r := range input {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	number := digits.String()
	if len(number) < 8 || len(number) > 15 {
		return "", "", errors.New("Type the full number, with country and area code.")
	}
	cli := d.client()
	if cli == nil || !cli.IsLoggedIn() || !cli.IsConnected() {
		return "", "", errors.New("Not connected.")
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	res, err := cli.IsOnWhatsApp(ctx, []string{"+" + number})
	if err != nil {
		return "", "", fmt.Errorf("Could not check the number: %w", err)
	}
	if len(res) == 0 || !res[0].IsIn {
		return "", "", fmt.Errorf("+%s is not on WhatsApp.", number)
	}
	found := res[0].JID
	if found.Server != types.DefaultUserServer && !res[0].PhoneNumber.IsEmpty() {
		found = res[0].PhoneNumber
	}
	found = d.canonical(found, res[0].PhoneNumber)
	name = d.contactName(cli, found)
	if name == "" && res[0].VerifiedName != nil && res[0].VerifiedName.Details != nil {
		name = res[0].VerifiedName.Details.GetVerifiedName()
	}
	return found.String(), name, nil
}
