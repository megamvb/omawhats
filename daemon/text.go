package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"unicode"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func withCaption(label, caption string) string {
	caption = strings.TrimSpace(caption)
	if caption == "" {
		return label
	}
	return label + " · " + caption
}

// firstText is the first candidate that says something. Business messages put
// their text in whichever field the sender felt like using, so asking for one
// field and giving up is how a message ends up looking empty.
func firstText(candidates ...string) string {
	for _, c := range candidates {
		if s := strings.TrimSpace(c); s != "" {
			return s
		}
	}
	return ""
}

// describe turns a message into the one line of text this client can show, and
// a coarse kind. Media is never downloaded — it is named, with its caption.
//
// An empty text means "not a line in the conversation": a reaction, a protocol
// message, a poll vote, key distribution. Everything else always gets a line,
// including kinds this client cannot draw, because dropping a message is worse
// than an ugly placeholder — the message is on the phone, and a hole here reads
// as the client being out of sync with it.
func describe(m *waE2E.Message) (text, kind string) {
	if m == nil {
		return "", ""
	}
	switch {
	case m.GetConversation() != "":
		return m.GetConversation(), "text"
	case m.GetExtendedTextMessage() != nil:
		e := m.GetExtendedTextMessage()
		// A shared link with no words of its own is still a message.
		return firstText(e.GetText(), e.GetMatchedText(), e.GetTitle()), "text"
	case m.GetImageMessage() != nil:
		return withCaption("📷 Photo", m.GetImageMessage().GetCaption()), "image"
	case m.GetVideoMessage() != nil:
		return withCaption("🎥 Video", m.GetVideoMessage().GetCaption()), "video"
	case m.GetPtvMessage() != nil:
		return "🎥 Video note", "video"
	case m.GetAudioMessage() != nil:
		a := m.GetAudioMessage()
		label := "🎵 Audio"
		if a.GetPTT() {
			label = "🎤 Voice message"
		}
		if s := a.GetSeconds(); s > 0 {
			label += fmt.Sprintf(" (%d:%02d)", s/60, s%60)
		}
		return label, "audio"
	case m.GetDocumentMessage() != nil:
		d := m.GetDocumentMessage()
		name := d.GetFileName()
		if name == "" {
			name = d.GetTitle()
		}
		if name == "" {
			name = "Document"
		}
		return withCaption("📄 "+name, d.GetCaption()), "document"
	case m.GetDocumentWithCaptionMessage() != nil:
		return describe(m.GetDocumentWithCaptionMessage().GetMessage())
	case m.GetStickerMessage() != nil:
		return "🏷 Sticker", "sticker"
	case m.GetStickerPackMessage() != nil:
		p := m.GetStickerPackMessage()
		return withCaption("🏷 Sticker pack · "+firstText(p.GetName(), "pack"), p.GetCaption()), "sticker"
	case m.GetContactMessage() != nil:
		return "👤 " + m.GetContactMessage().GetDisplayName(), "contact"
	case m.GetContactsArrayMessage() != nil:
		return fmt.Sprintf("👥 %d contacts", len(m.GetContactsArrayMessage().GetContacts())), "contact"
	case m.GetLocationMessage() != nil:
		l := m.GetLocationMessage()
		label := "📍 Location"
		if l.GetName() != "" {
			label += " · " + l.GetName()
		}
		return label, "location"
	case m.GetLiveLocationMessage() != nil:
		return "📍 Live location", "location"
	// Polls: V4 wraps another message and is unwrapped further down, the rest
	// are the same struct under five field numbers.
	case m.GetPollCreationMessage() != nil, m.GetPollCreationMessageV2() != nil, m.GetPollCreationMessageV3() != nil,
		m.GetPollCreationMessageV5() != nil, m.GetPollCreationMessageV6() != nil:
		return "📊 " + firstText(
			m.GetPollCreationMessage().GetName(),
			m.GetPollCreationMessageV2().GetName(),
			m.GetPollCreationMessageV3().GetName(),
			m.GetPollCreationMessageV5().GetName(),
			m.GetPollCreationMessageV6().GetName(),
			"Poll"), "poll"
	case m.GetAlbumMessage() != nil:
		a := m.GetAlbumMessage()
		// The pictures themselves arrive as their own messages; this is the
		// header that says how many are coming.
		if n := a.GetExpectedImageCount() + a.GetExpectedVideoCount(); n > 0 {
			return fmt.Sprintf("🖼 Album (%d)", n), "album"
		}
		return "🖼 Album", "album"
	case m.GetEventMessage() != nil:
		e := m.GetEventMessage()
		label := "📅 " + firstText(e.GetName(), "Event")
		if e.GetIsCanceled() {
			label += " (canceled)"
		}
		return withCaption(label, e.GetDescription()), "event"
	case m.GetEventInviteMessage() != nil:
		e := m.GetEventInviteMessage()
		return withCaption("📅 "+firstText(e.GetEventTitle(), "Event invite"), e.GetCaption()), "event"
	case m.GetGroupInviteMessage() != nil:
		g := m.GetGroupInviteMessage()
		return withCaption("👥 Group invite · "+firstText(g.GetGroupName(), "group"), g.GetCaption()), "invite"
	case m.GetCallLogMesssage() != nil:
		c := m.GetCallLogMesssage()
		label := "📞 Voice call"
		if c.GetIsVideo() {
			label = "🎥 Video call"
		}
		label += " · " + strings.ToLower(strings.ReplaceAll(c.GetCallOutcome().String(), "_", " "))
		if s := c.GetDurationSecs(); s > 0 {
			label += fmt.Sprintf(" (%d:%02d)", s/60, s%60)
		}
		return label, "call"
	case m.GetMusicMessage() != nil:
		e := m.GetMusicMessage().GetEmbeddedMusic()
		label := "🎵 " + firstText(e.GetTitle(), "Music")
		if a := e.GetAuthor(); a != "" {
			label += " · " + a
		}
		return label, "audio"
	case m.GetRequestPhoneNumberMessage() != nil:
		return "📱 Asked for your phone number", "request"
	// Business messages: buttons, lists, catalogues and the answers to them.
	// The words are what matters here; the buttons cannot be pressed anyway.
	case m.GetTemplateMessage() != nil:
		t := m.GetTemplateMessage().GetHydratedTemplate()
		return firstText(t.GetHydratedContentText(), t.GetHydratedTitleText(), t.GetHydratedFooterText(),
			"💬 Message with buttons"), "text"
	case m.GetButtonsMessage() != nil:
		b := m.GetButtonsMessage()
		return firstText(b.GetContentText(), b.GetText(), b.GetFooterText(), "💬 Message with buttons"), "text"
	case m.GetInteractiveMessage() != nil:
		i := m.GetInteractiveMessage()
		return firstText(i.GetBody().GetText(), i.GetHeader().GetTitle(), i.GetHeader().GetSubtitle(),
			i.GetFooter().GetText(), "💬 Interactive message"), "text"
	case m.GetListMessage() != nil:
		l := m.GetListMessage()
		return withCaption("📋 "+firstText(l.GetTitle(), "List"), l.GetDescription()), "text"
	case m.GetTemplateButtonReplyMessage() != nil:
		return firstText(m.GetTemplateButtonReplyMessage().GetSelectedDisplayText(), "💬 Button reply"), "text"
	case m.GetButtonsResponseMessage() != nil:
		return firstText(m.GetButtonsResponseMessage().GetSelectedDisplayText(), "💬 Button reply"), "text"
	case m.GetListResponseMessage() != nil:
		l := m.GetListResponseMessage()
		return firstText(l.GetTitle(), l.GetDescription(), l.GetSingleSelectReply().GetSelectedRowID(),
			"💬 List reply"), "text"
	case m.GetInteractiveResponseMessage() != nil:
		return firstText(m.GetInteractiveResponseMessage().GetBody().GetText(), "💬 Reply"), "text"
	case m.GetProductMessage() != nil:
		p := m.GetProductMessage()
		return withCaption("🛒 "+firstText(p.GetProduct().GetTitle(), "Product"),
			firstText(p.GetBody(), p.GetProduct().GetDescription())), "product"
	case m.GetOrderMessage() != nil:
		o := m.GetOrderMessage()
		label := "🧾 " + firstText(o.GetOrderTitle(), "Order")
		if n := o.GetItemCount(); n > 0 {
			label += fmt.Sprintf(" (%d items)", n)
		}
		return withCaption(label, o.GetMessage()), "order"
	}
	if inner := futureProof(m); inner != nil {
		return describe(inner)
	}
	return unsupported(m)
}

// futureProof unwraps a field that holds nothing but another message. That is
// how WhatsApp ships a new kind without breaking clients that predate it — a
// poll V4, a question, a spoiler are all an ordinary message in a wrapper — so
// recursing finds what the wrapper was hiding instead of calling it unknown.
func futureProof(m *waE2E.Message) *waE2E.Message {
	var inner *waE2E.Message
	fields := m.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len() && inner == nil; i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() || !m.ProtoReflect().Has(fd) {
			continue
		}
		if w, ok := m.ProtoReflect().Get(fd).Message().Interface().(*waE2E.FutureProofMessage); ok {
			inner = w.GetMessage()
		}
	}
	return inner
}

// silentFields are the fields that mean no line in the conversation: the ones
// describe already looked at and found empty, whatever belongs to another
// message (a reaction, a vote, an edit), the keys, and the status and channel
// traffic that never reaches a chat this client lists. Anything outside this
// set is a message a person sent, so it gets a placeholder rather than silence.
var silentFields = map[string]bool{
	// handled above, or deliberately not a line of its own
	"conversation": true, "extendedTextMessage": true, "imageMessage": true, "videoMessage": true,
	"ptvMessage": true, "audioMessage": true, "documentMessage": true, "documentWithCaptionMessage": true,
	"stickerMessage": true, "stickerPackMessage": true, "contactMessage": true, "contactsArrayMessage": true,
	"locationMessage": true, "liveLocationMessage": true, "pollCreationMessage": true,
	"pollCreationMessageV2": true, "pollCreationMessageV3": true, "pollCreationMessageV5": true,
	"pollCreationMessageV6": true, "albumMessage": true, "eventMessage": true, "eventInviteMessage": true,
	"groupInviteMessage": true, "callLogMesssage": true, "musicMessage": true,
	"requestPhoneNumberMessage": true, "templateMessage": true, "buttonsMessage": true,
	"interactiveMessage": true, "listMessage": true, "templateButtonReplyMessage": true,
	"buttonsResponseMessage": true, "listResponseMessage": true, "interactiveResponseMessage": true,
	"productMessage": true, "orderMessage": true,
	// about another message, or about nothing a reader sees
	"protocolMessage": true, "reactionMessage": true, "encReactionMessage": true, "encCommentMessage": true,
	"encEventResponseMessage": true, "pollUpdateMessage": true, "pollResultSnapshotMessage": true,
	"pollResultSnapshotMessageV3": true, "pollAddOptionMessage": true, "pollCreationOptionImageMessage": true,
	"keepInChatMessage": true, "pinInChatMessage": true, "eventCoverImage": true, "commentMessage": true,
	"messageContextInfo": true, "contextInfo": true, "deviceSentMessage": true, "call": true, "chat": true,
	"groupMentionedMessage": true, "limitSharingMessage": true, "urlTrackingMap": true,
	// keys, plumbing and recovery
	"senderKeyDistributionMessage": true, "fastRatchetKeySenderKeyDistributionMessage": true,
	"stickerSyncRmrMessage": true, "placeholderMessage": true, "secretEncryptedMessage": true,
	"messageHistoryBundle": true, "messageHistoryNotice": true, "botTaskMessage": true,
	"botPlatformRegistrationSuccessMessage": true, "groupRootKeyShare": true,
	"rootSecretDistributeMessage": true, "acp2SettingMessage": true,
	// status ("stories") and channels, which this client does not list at all
	"statusMentionMessage": true, "groupStatusMentionMessage": true, "statusAddYours": true,
	"groupStatusMessage": true, "groupStatusMessageV2": true, "statusNotificationMessage": true,
	"statusQuestionAnswerMessage": true, "statusQuotedMessage": true, "statusStickerInteractionMessage": true,
	"statusLinkPreviewMetadata": true, "newsletterAdminInviteMessage": true,
	"newsletterAdminProfileMessage": true, "newsletterAdminProfileStatusMessage": true,
	"newsletterFollowerInviteMessageV2": true, "newsletterScheduledMessage": true,
}

// unsupported names a message this client has no drawing for. It is a line all
// the same: the reader learns something arrived and can open the phone, instead
// of staring at a conversation that quietly disagrees with it.
func unsupported(m *waE2E.Message) (text, kind string) {
	fields := m.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		name := string(fd.Name())
		if silentFields[name] || !m.ProtoReflect().Has(fd) {
			continue
		}
		noteUnsupported(name)
		return "❔ Unsupported: " + spacedName(name), "unsupported"
	}
	return "", ""
}

var unsupportedSeen sync.Map

// noteUnsupported logs each unknown kind once, so the log says what is worth
// teaching describe next without one chatty sender filling it.
func noteUnsupported(field string) {
	if _, seen := unsupportedSeen.LoadOrStore(field, true); !seen {
		log.Printf("no support for %s yet; showing a placeholder", field)
	}
}

// spacedName turns a protobuf field name into something readable:
// "requestPaymentMessage" becomes "request payment".
func spacedName(field string) string {
	var b strings.Builder
	for i, r := range strings.ReplaceAll(field, "Message", "") {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteRune(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimSpace(b.String())
}

// contextOf finds the ContextInfo of a message — where a reply names the
// message it answers. Every kind of message (text, photo, audio, …) carries it
// in its own field of the same name, so it is looked up by name.
func contextOf(m *waE2E.Message) *waE2E.ContextInfo {
	if m == nil {
		return nil
	}
	if inner := m.GetDocumentWithCaptionMessage().GetMessage(); inner != nil {
		return contextOf(inner)
	}
	var ci *waE2E.ContextInfo
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
			return true
		}
		sub := v.Message()
		f := sub.Descriptor().Fields().ByName("contextInfo")
		if f == nil || !sub.Has(f) {
			return true
		}
		if c, ok := sub.Get(f).Message().Interface().(*waE2E.ContextInfo); ok && c.GetStanzaID() != "" {
			ci = c
			return false
		}
		return true
	})
	return ci
}

// protocolOf finds an edit/revoke instruction, whichever wrapper it came in.
func protocolOf(m *waE2E.Message) *waE2E.ProtocolMessage {
	if pm := m.GetProtocolMessage(); pm != nil {
		return pm
	}
	if pm := m.GetEditedMessage().GetMessage().GetProtocolMessage(); pm != nil {
		return pm
	}
	return nil
}

// preview is the chat-list line: newlines folded, capped so one long message
// does not bloat every chats broadcast.
func preview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return text
}
