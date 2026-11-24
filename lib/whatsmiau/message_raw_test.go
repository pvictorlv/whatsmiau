package whatsmiau

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/verbeux-ai/whatsmiau/models"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// unmarshalRaw marshals a WookMessageRaw the way the webhook does and decodes it
// back into a generic map, which is what the consumer actually sees.
func unmarshalRaw(t *testing.T, raw *WookMessageRaw) map[string]any {
	t.Helper()

	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	return got
}

// TestParseWAMessageKeepsUnmappedTypes garante que um tipo sem mapeamento
// explícito (templateMessage) chega ao consumidor em vez de virar "unknown".
func TestParseWAMessageKeepsUnmappedTypes(t *testing.T) {
	s := &Whatsmiau{}
	msg := &waE2E.Message{
		TemplateMessage: &waE2E.TemplateMessage{
			HydratedTemplate: &waE2E.TemplateMessage_HydratedFourRowTemplate{
				HydratedContentText: proto.String("confirme seu pedido"),
			},
		},
	}

	messageType, raw, _ := s.parseWAMessage(msg)

	if messageType != "templateMessage" {
		t.Errorf("expected messageType templateMessage, got %q", messageType)
	}

	got := unmarshalRaw(t, raw)
	template, ok := got["templateMessage"].(map[string]any)
	if !ok {
		t.Fatalf("templateMessage missing from payload: %v", got)
	}

	hydrated, ok := template["hydratedTemplate"].(map[string]any)
	if !ok {
		t.Fatalf("hydratedTemplate missing: %v", template)
	}
	if hydrated["hydratedContentText"] != "confirme seu pedido" {
		t.Errorf("template content not preserved: %v", hydrated)
	}
}

// TestParseWAMessageEditReachesConsumer trava o contrato que o Zapeada usa para
// aplicar edições: message.protocolMessage.editedMessage.conversation.
func TestParseWAMessageEditReachesConsumer(t *testing.T) {
	s := &Whatsmiau{}
	msg := &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key: &waCommon.MessageKey{
				ID:        proto.String("ORIGINAL_ID"),
				RemoteJID: proto.String("5511999999999@s.whatsapp.net"),
			},
			EditedMessage: &waE2E.Message{
				Conversation: proto.String("texto corrigido"),
			},
		},
	}

	messageType, raw, _ := s.parseWAMessage(msg)

	if messageType != "protocolMessage" {
		t.Errorf("expected messageType protocolMessage, got %q", messageType)
	}

	got := unmarshalRaw(t, raw)
	protocol, ok := got["protocolMessage"].(map[string]any)
	if !ok {
		t.Fatalf("protocolMessage missing from payload: %v", got)
	}

	edited, ok := protocol["editedMessage"].(map[string]any)
	if !ok {
		t.Fatalf("editedMessage missing: %v", protocol)
	}
	if edited["conversation"] != "texto corrigido" {
		t.Errorf("edited body not preserved: %v", edited)
	}

	// Zapeada reads protocol.key.id / .remoteJid (Baileys spelling). protojson
	// would emit "ID" / "remoteJID", so this asserts the normalization.
	key, ok := protocol["key"].(map[string]any)
	if !ok {
		t.Fatalf("target key missing: %v", protocol)
	}
	if key["id"] != "ORIGINAL_ID" {
		t.Errorf("expected lowerCamel key.id, got %v", key)
	}
	if key["remoteJid"] != "5511999999999@s.whatsapp.net" {
		t.Errorf("expected lowerCamel key.remoteJid, got %v", key)
	}
}

func TestNormalizeProtoKey(t *testing.T) {
	cases := map[string]string{
		"ID":                "id",
		"remoteJID":         "remoteJid",
		"URL":               "url",
		"fileSHA256":        "fileSha256",
		"fileEncSHA256":     "fileEncSha256",
		"JPEGThumbnail":     "jpegThumbnail",
		"senderTimestampMS": "senderTimestampMs",
		"encIV":             "encIv",
		"PTT":               "ptt",
		"mediaKey":          "mediaKey",
		"conversation":      "conversation",
		"gifPlayback":       "gifPlayback",
		"selectedRowID":     "selectedRowId",
	}

	for input, want := range cases {
		if got := normalizeProtoKey(input); got != want {
			t.Errorf("normalizeProtoKey(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestPartialMappingKeepsFallbackFields cobre o caso em que o mapeamento à mão
// é incompleto: os campos mapeados continuam valendo e os que faltavam passam a
// aparecer, em vez de o mapeamento parcial sombrear o payload completo.
func TestPartialMappingKeepsFallbackFields(t *testing.T) {
	s := &Whatsmiau{}
	msg := &waE2E.Message{
		ListResponseMessage: &waE2E.ListResponseMessage{
			Title:    proto.String("Escolha uma opção"),
			ListType: waE2E.ListResponseMessage_SINGLE_SELECT.Enum(),
			SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{
				SelectedRowID: proto.String("row_2"),
			},
		},
	}

	messageType, raw, _ := s.parseWAMessage(msg)
	if messageType != "listResponseMessage" {
		t.Fatalf("expected listResponseMessage, got %q", messageType)
	}

	got := unmarshalRaw(t, raw)
	list, ok := got["listResponseMessage"].(map[string]any)
	if !ok {
		t.Fatalf("listResponseMessage missing: %v", got)
	}

	// Mapped explicitly today.
	if list["listType"] != "SINGLE_SELECT" {
		t.Errorf("listType lost: %v", list)
	}
	reply, ok := list["singleSelectReply"].(map[string]any)
	if !ok {
		t.Fatalf("singleSelectReply missing: %v", list)
	}
	if reply["selectedRowId"] != "row_2" {
		t.Errorf("selectedRowId lost: %v", reply)
	}

	// Never mapped, and read by the consumer — must come from the fallback.
	if list["title"] != "Escolha uma opção" {
		t.Errorf("title should be filled in from the fallback: %v", list)
	}
}

// TestMappedTypeWinsOverFallback garante que o campo mapeado à mão (que carrega
// mediaUrl/base64) não é substituído pela versão crua do protojson, e que ele
// aparece antes na ordem — consumidores pegam a primeira chave de tipo.
func TestMappedTypeWinsOverFallback(t *testing.T) {
	s := &Whatsmiau{}
	msg := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Mimetype: proto.String("image/jpeg"),
			Caption:  proto.String("olha isso"),
		},
	}

	messageType, raw, _ := s.parseWAMessage(msg)
	if messageType != "imageMessage" {
		t.Fatalf("expected imageMessage, got %q", messageType)
	}

	raw.MediaURL = "https://storage.example/img.jpg"

	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	image, ok := got["imageMessage"].(map[string]any)
	if !ok {
		t.Fatalf("imageMessage missing: %v", got)
	}
	if image["caption"] != "olha isso" {
		t.Errorf("expected the mapped struct, got the protojson one: %v", image)
	}
	if got["mediaUrl"] != "https://storage.example/img.jpg" {
		t.Errorf("mediaUrl lost: %v", got)
	}
}

// TestRawFallbackDropsCryptoNoise garante que o ruído de Signal não engorda o
// payload nem confunde a detecção de tipo do consumidor.
func TestRawFallbackDropsCryptoNoise(t *testing.T) {
	s := &Whatsmiau{}
	msg := &waE2E.Message{
		Conversation: proto.String("oi"),
		SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{
			GroupID: proto.String("123@g.us"),
		},
	}

	_, raw, _ := s.parseWAMessage(msg)

	got := unmarshalRaw(t, raw)
	if _, present := got["senderKeyDistributionMessage"]; present {
		t.Errorf("senderKeyDistributionMessage should be stripped: %v", got)
	}
	if got["conversation"] != "oi" {
		t.Errorf("conversation lost: %v", got)
	}
}

// TestUnknownStaysUnknown garante que uma mensagem realmente vazia continua
// sinalizada como unknown em vez de inventar um tipo.
func TestUnknownStaysUnknown(t *testing.T) {
	s := &Whatsmiau{}

	messageType, _, _ := s.parseWAMessage(&waE2E.Message{})

	if messageType != "unknown" {
		t.Errorf("expected unknown, got %q", messageType)
	}
}

// TestConvertEventMessageCarriesNonJidMentions trava o contrato do @todos: o
// WhatsApp manda a menção coletiva só como contagem em nonJidMentions, sem
// nenhum item em mentionedJid, e o backend precisa dela para reconhecer.
func TestConvertEventMessageCarriesNonJidMentions(t *testing.T) {
	s := &Whatsmiau{clients: xsync.NewMap[string, *whatsmeow.Client]()}
	s.clients.Store("instance-1", &whatsmeow.Client{})

	build := func(nonJID uint32) *WookMessageData {
		msg := &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String("@todos reunião"),
				ContextInfo: &waE2E.ContextInfo{NonJIDMentions: proto.Uint32(nonJID)},
			},
		}
		evt := &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{
					Chat:    types.NewJID("120363000000000000", types.GroupServer),
					Sender:  types.NewJID("5511999999999", types.DefaultUserServer),
					IsGroup: true,
				},
				ID:        "msg-1",
				Timestamp: time.Now(),
			},
			Message:    msg,
			RawMessage: msg,
		}
		return s.convertEventMessage("instance-1", &models.Instance{ID: "instance-1"}, evt)
	}

	data := build(1)
	if data == nil || data.ContextInfo == nil {
		t.Fatalf("expected contextInfo in converted message, got %#v", data)
	}
	if data.ContextInfo.NonJidMentions != 1 {
		t.Fatalf("expected nonJidMentions=1, got %d", data.ContextInfo.NonJidMentions)
	}
	if len(data.ContextInfo.MentionedJid) != 0 {
		t.Fatalf("expected no mentionedJid for a collective mention, got %v", data.ContextInfo.MentionedJid)
	}
	encoded, err := json.Marshal(data.ContextInfo)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(encoded), `"nonJidMentions":1`) {
		t.Fatalf("nonJidMentions missing from payload: %s", encoded)
	}

	if zero := build(0); zero != nil && zero.ContextInfo != nil {
		encoded, _ := json.Marshal(zero.ContextInfo)
		if strings.Contains(string(encoded), "nonJidMentions") {
			t.Fatalf("nonJidMentions must be omitted when zero: %s", encoded)
		}
	}
}
