package main

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// The kinds describe draws itself, including the ones it learned late: a
// business message hides its text a level down, and a poll V4 hides the whole
// poll one wrapper down.
func TestDescribeKinds(t *testing.T) {
	cases := []struct {
		name string
		msg  *waE2E.Message
		text string
		kind string
	}{
		{"text", &waE2E.Message{Conversation: proto.String("hello")}, "hello", "text"},
		{"link with no words", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			MatchedText: proto.String("https://example.com")}}, "https://example.com", "text"},
		{"photo", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look")}},
			"📷 Photo · look", "image"},
		{"poll", &waE2E.Message{PollCreationMessageV6: &waE2E.PollCreationMessage{
			Name: proto.String("Lunch?")}}, "📊 Lunch?", "poll"},
		{"poll v4 in its wrapper", &waE2E.Message{PollCreationMessageV4: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{
				Name: proto.String("Lunch?")}}}}, "📊 Lunch?", "poll"},
		{"album", &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{
			ExpectedImageCount: proto.Uint32(3), ExpectedVideoCount: proto.Uint32(1)}}, "🖼 Album (4)", "album"},
		{"group event", &waE2E.Message{EventMessage: &waE2E.EventMessage{
			Name: proto.String("Barbecue"), IsCanceled: proto.Bool(true)}}, "📅 Barbecue (canceled)", "event"},
		{"group invite", &waE2E.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{
			GroupName: proto.String("Neighbours"), Caption: proto.String("come in")}},
			"👥 Group invite · Neighbours · come in", "invite"},
		{"missed call", &waE2E.Message{CallLogMesssage: &waE2E.CallLogMessage{
			CallOutcome: waE2E.CallLogMessage_MISSED.Enum()}}, "📞 Voice call · missed", "call"},
		{"business template", &waE2E.Message{TemplateMessage: &waE2E.TemplateMessage{
			HydratedTemplate: &waE2E.TemplateMessage_HydratedFourRowTemplate{
				HydratedContentText: proto.String("Your order is on its way")}}},
			"Your order is on its way", "text"},
		{"business buttons with only a footer", &waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{
			FooterText: proto.String("Reply 1 or 2")}}, "Reply 1 or 2", "text"},
		{"answer to a button", &waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{
			Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Yes"}}},
			"Yes", "text"},
		{"list from a business", &waE2E.Message{ListMessage: &waE2E.ListMessage{
			Title: proto.String("Menu"), Description: proto.String("pick one")}}, "📋 Menu · pick one", "text"},
		{"order", &waE2E.Message{OrderMessage: &waE2E.OrderMessage{
			OrderTitle: proto.String("Pizza"), ItemCount: proto.Int32(2)}}, "🧾 Pizza (2 items)", "order"},
	}
	for _, c := range cases {
		text, kind := describe(c.msg)
		if text != c.text || kind != c.kind {
			t.Errorf("%s: got %q/%q, want %q/%q", c.name, text, kind, c.text, c.kind)
		}
	}
}

// A kind describe knows nothing about must still become a line. Dropping it is
// what makes the client look out of sync with the phone.
func TestDescribeUnknownKindIsStillALine(t *testing.T) {
	text, kind := describe(&waE2E.Message{RequestPaymentMessage: &waE2E.RequestPaymentMessage{
		Amount1000: proto.Uint64(5000)}})
	if want := "❔ Unsupported: request payment"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if kind != "unsupported" {
		t.Errorf("kind = %q, want unsupported", kind)
	}
	// Whatever else rides along, the message itself is what names the line.
	text, _ = describe(&waE2E.Message{
		MessageContextInfo:           &waE2E.MessageContextInfo{},
		SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{},
		InvoiceMessage:               &waE2E.InvoiceMessage{},
	})
	if want := "❔ Unsupported: invoice"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

// Everything that is about another message, or about no message at all, stays
// out of the conversation — a placeholder for each of these would be noise.
func TestDescribeSilentKinds(t *testing.T) {
	silent := map[string]*waE2E.Message{
		"reaction":     {ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}},
		"protocol":     {ProtocolMessage: &waE2E.ProtocolMessage{}},
		"keys":         {SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{}},
		"context only": {MessageContextInfo: &waE2E.MessageContextInfo{}},
		"poll vote":    {PollUpdateMessage: &waE2E.PollUpdateMessage{}},
		"pin in chat":  {PinInChatMessage: &waE2E.PinInChatMessage{}},
		"empty text":   {Conversation: proto.String("")},
		"nothing":      {},
	}
	for name, msg := range silent {
		if text, kind := describe(msg); text != "" || kind != "" {
			t.Errorf("%s: got %q/%q, want both empty", name, text, kind)
		}
	}
	if text, _ := describe(nil); text != "" {
		t.Errorf("nil message: got %q", text)
	}
}

func TestSpacedName(t *testing.T) {
	for field, want := range map[string]string{
		"requestPaymentMessage":    "request payment",
		"invoiceMessage":           "invoice",
		"scheduledCallEditMessage": "scheduled call edit",
		"pollCreationMessageV4":    "poll creation v4",
	} {
		if got := spacedName(field); got != want {
			t.Errorf("spacedName(%q) = %q, want %q", field, got, want)
		}
	}
}
