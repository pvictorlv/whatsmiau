package whatsmiau

import (
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func secretEditTarget() *waCommon.MessageKey {
	return &waCommon.MessageKey{
		RemoteJID:   proto.String("120363000000000000@g.us"),
		FromMe:      proto.Bool(false),
		ID:          proto.String("ORIGINAL_ID"),
		Participant: proto.String("5511999999999@s.whatsapp.net"),
	}
}

func TestSecretEditAsProtocolMessageBareContent(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	got := secretEditAsProtocolMessage(secretEditTarget(), &waE2E.Message{Conversation: proto.String("texto corrigido")}, at)

	pm := got.GetProtocolMessage()
	if pm.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT {
		t.Fatalf("expected MESSAGE_EDIT, got %v", pm.GetType())
	}
	if pm.GetKey().GetID() != "ORIGINAL_ID" || pm.GetKey().GetParticipant() != "5511999999999@s.whatsapp.net" {
		t.Fatalf("target key not preserved: %v", pm.GetKey())
	}
	if pm.GetEditedMessage().GetConversation() != "texto corrigido" {
		t.Fatalf("edited content lost: %v", pm.GetEditedMessage())
	}
	if pm.GetTimestampMS() != at.UnixMilli() {
		t.Fatalf("expected timestamp %d, got %d", at.UnixMilli(), pm.GetTimestampMS())
	}
}

func TestSecretEditAsProtocolMessageWrappedContent(t *testing.T) {
	decrypted := &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			EditedMessage: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("com link")}},
		},
	}

	got := secretEditAsProtocolMessage(secretEditTarget(), decrypted, time.Now())

	if got.GetProtocolMessage().GetEditedMessage().GetExtendedTextMessage().GetText() != "com link" {
		t.Fatalf("wrapped content not unwrapped: %v", got)
	}
}

func TestSecretEditAsProtocolMessageFallsBackToInnerKey(t *testing.T) {
	decrypted := &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Key:           secretEditTarget(),
			EditedMessage: &waE2E.Message{Conversation: proto.String("x")},
		},
	}

	got := secretEditAsProtocolMessage(nil, decrypted, time.Now())

	if got.GetProtocolMessage().GetKey().GetID() != "ORIGINAL_ID" {
		t.Fatalf("expected inner key as target, got %v", got.GetProtocolMessage().GetKey())
	}
}

func TestSecretEditAsProtocolMessageWithoutTarget(t *testing.T) {
	if got := secretEditAsProtocolMessage(nil, &waE2E.Message{Conversation: proto.String("x")}, time.Now()); got != nil {
		t.Fatalf("edit without a target must be dropped, got %v", got)
	}
	if got := secretEditAsProtocolMessage(&waCommon.MessageKey{}, &waE2E.Message{Conversation: proto.String("x")}, time.Now()); got != nil {
		t.Fatalf("edit with an empty target id must be dropped, got %v", got)
	}
}

func TestUnwrapSecretEditLeavesOtherMessagesAlone(t *testing.T) {
	s := &Whatsmiau{clients: xsync.NewMap[string, *whatsmeow.Client]()}
	evt := &events.Message{
		Info:    types.MessageInfo{ID: "ABC"},
		Message: &waE2E.Message{Conversation: proto.String("oi")},
	}

	if got := s.unwrapSecretEdit("instance", evt); got != evt {
		t.Fatal("a regular message must pass through untouched")
	}
}

func TestUnwrapSecretEditWithoutClientKeepsEnvelope(t *testing.T) {
	s := &Whatsmiau{clients: xsync.NewMap[string, *whatsmeow.Client]()}
	evt := &events.Message{
		Info: types.MessageInfo{ID: "ABC"},
		Message: &waE2E.Message{
			SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
				SecretEncType:    waE2E.SecretEncryptedMessage_MESSAGE_EDIT.Enum(),
				TargetMessageKey: secretEditTarget(),
			},
		},
	}

	if got := s.unwrapSecretEdit("instance", evt); got != evt {
		t.Fatal("an edit that cannot be decrypted must reach the consumer unchanged")
	}
}
