package whatsmiau

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestBuildTextMessagePlain(t *testing.T) {
	msg := buildTextMessage("oi", nil)

	if msg.GetConversation() != "oi" {
		t.Fatalf("conversation = %q, want %q", msg.GetConversation(), "oi")
	}
	if msg.ExtendedTextMessage != nil {
		t.Fatal("plain text must not carry an ExtendedTextMessage")
	}
}

// Regressão: a citação saía com `Conversation` + ExtendedTextMessage vazio, e o
// app do celular mostrava a mensagem em branco.
func TestBuildTextMessageQuoted(t *testing.T) {
	sender := types.NewJID("559281358585", types.DefaultUserServer)
	msg := buildTextMessage("resposta", &textQuote{ID: "A5C6E2C8", Text: "pergunta", Sender: sender})

	if msg.Conversation != nil {
		t.Fatal("quoted text must not set Conversation")
	}
	ext := msg.GetExtendedTextMessage()
	if ext.GetText() != "resposta" {
		t.Fatalf("text = %q, want %q", ext.GetText(), "resposta")
	}
	ci := ext.GetContextInfo()
	if ci.GetStanzaID() != "A5C6E2C8" {
		t.Fatalf("stanzaID = %q", ci.GetStanzaID())
	}
	if ci.GetParticipant() != "559281358585@s.whatsapp.net" {
		t.Fatalf("participant = %q", ci.GetParticipant())
	}
	if ci.GetQuotedMessage().GetConversation() != "pergunta" {
		t.Fatalf("quoted = %q", ci.GetQuotedMessage().GetConversation())
	}
}
