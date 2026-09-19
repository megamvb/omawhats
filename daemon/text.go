package main

import (
	"fmt"
	"strings"

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

// describe turns a message into the one line of text this client can show, and
// a coarse kind. Media is never downloaded — it is named, with its caption.
// An empty text means "not a message a person would see in the chat list"
// (reactions, protocol messages, key distribution, and so on).
func describe(m *waE2E.Message) (text, kind string) {
	if m == nil {
		return "", ""
	}
	switch {
	case m.GetConversation() != "":
		return m.GetConversation(), "text"
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText(), "text"
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
	case m.GetPollCreationMessage() != nil:
		return "📊 " + m.GetPollCreationMessage().GetName(), "poll"
	case m.GetPollCreationMessageV2() != nil:
		return "📊 " + m.GetPollCreationMessageV2().GetName(), "poll"
	case m.GetPollCreationMessageV3() != nil:
		return "📊 " + m.GetPollCreationMessageV3().GetName(), "poll"
	}
	return "", ""
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
