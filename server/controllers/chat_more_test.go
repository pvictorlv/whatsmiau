package controllers

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/verbeux-ai/whatsmiau/server/dto"
	"go.mau.fi/whatsmeow"
)

func TestSelectMediaLocatorPicksCorrectType(t *testing.T) {
	cases := []struct {
		name     string
		content  dto.GetBase64Content
		wantType whatsmeow.MediaType
		wantNil  bool
	}{
		{"image", dto.GetBase64Content{ImageMessage: &dto.MediaLocator{Mimetype: "image/jpeg"}}, whatsmeow.MediaImage, false},
		{"sticker", dto.GetBase64Content{StickerMessage: &dto.MediaLocator{}}, whatsmeow.MediaImage, false},
		{"video", dto.GetBase64Content{VideoMessage: &dto.MediaLocator{}}, whatsmeow.MediaVideo, false},
		{"ptv", dto.GetBase64Content{PtvMessage: &dto.MediaLocator{}}, whatsmeow.MediaVideo, false},
		{"audio", dto.GetBase64Content{AudioMessage: &dto.MediaLocator{}}, whatsmeow.MediaAudio, false},
		{"document", dto.GetBase64Content{DocumentMessage: &dto.MediaLocator{}}, whatsmeow.MediaDocument, false},
		{"empty", dto.GetBase64Content{}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locator, mediaType, _ := selectMediaLocator(&tc.content)
			if tc.wantNil {
				if locator != nil {
					t.Fatalf("expected nil locator, got %+v", locator)
				}
				return
			}
			if locator == nil {
				t.Fatalf("expected a locator, got nil")
			}
			if mediaType != tc.wantType {
				t.Errorf("expected media type %q, got %q", tc.wantType, mediaType)
			}
		})
	}
}

// O CRM devolve a mensagem inteira do webhook, com os blocos de mídia debaixo de
// "message". Antes o binding só olhava a raiz e toda recuperação de mídia
// terminava em "no downloadable media found".
func TestGetBase64RequestAcceptsEvolutionEnvelope(t *testing.T) {
	body := []byte(`{
		"message": {
			"key": {"id": "3EB0ABC", "remoteJid": "5511999999999@s.whatsapp.net", "fromMe": false},
			"message": {
				"imageMessage": {
					"directPath": "/v/t62.7118-24/foo",
					"mediaKey": "a2V5",
					"mimetype": "image/jpeg"
				}
			}
		},
		"convertToMp4": false
	}`)

	var request dto.GetBase64Request
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	locator, mediaType, mimetype := selectMediaLocator(request.Message.Content())
	if locator == nil {
		t.Fatal("expected the nested imageMessage to be found")
	}
	if locator.DirectPath != "/v/t62.7118-24/foo" {
		t.Errorf("unexpected direct path: %q", locator.DirectPath)
	}
	if mediaType != whatsmeow.MediaImage {
		t.Errorf("expected image media type, got %q", mediaType)
	}
	if mimetype != "image/jpeg" {
		t.Errorf("unexpected mimetype: %q", mimetype)
	}

	target := mediaRetryTarget(request.Message.Key)
	if target == nil {
		t.Fatal("expected a media retry target from the message key")
	}
	if target.MessageID != "3EB0ABC" {
		t.Errorf("unexpected message id: %q", target.MessageID)
	}
	if target.Chat.String() != "5511999999999@s.whatsapp.net" {
		t.Errorf("unexpected chat jid: %q", target.Chat.String())
	}
}

// A forma achatada (blocos na raiz) continua valendo para chamadas diretas.
func TestGetBase64RequestAcceptsFlatShape(t *testing.T) {
	body := []byte(`{"message": {"audioMessage": {"directPath": "/v/audio", "mimetype": "audio/ogg"}}}`)

	var request dto.GetBase64Request
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	locator, mediaType, _ := selectMediaLocator(request.Message.Content())
	if locator == nil || locator.DirectPath != "/v/audio" {
		t.Fatalf("expected the flat audioMessage to be found, got %+v", locator)
	}
	if mediaType != whatsmeow.MediaAudio {
		t.Errorf("expected audio media type, got %q", mediaType)
	}
	if mediaRetryTarget(request.Message.Key) != nil {
		t.Error("expected no retry target when the request carries no key")
	}
}

func TestMediaRetryTargetRejectsUnusableKeys(t *testing.T) {
	cases := []struct {
		name string
		key  *dto.GetBase64Key
	}{
		{"nil", nil},
		{"no id", &dto.GetBase64Key{RemoteJid: "5511999999999@s.whatsapp.net"}},
		{"no remote jid", &dto.GetBase64Key{Id: "3EB0ABC"}},
		{"unparseable remote jid", &dto.GetBase64Key{Id: "3EB0ABC", RemoteJid: "not a jid"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaRetryTarget(tc.key); got != nil {
				t.Errorf("expected nil target, got %+v", got)
			}
		})
	}
}

func TestMediaRetryTargetKeepsGroupParticipant(t *testing.T) {
	target := mediaRetryTarget(&dto.GetBase64Key{
		Id:          "3EB0ABC",
		RemoteJid:   "120363000000000000@g.us",
		Participant: "5511999999999@s.whatsapp.net",
	})

	if target == nil {
		t.Fatal("expected a target for a group message")
	}
	if target.Sender.String() != "5511999999999@s.whatsapp.net" {
		t.Errorf("unexpected sender: %q", target.Sender.String())
	}
}

func TestDecodeB64(t *testing.T) {
	raw := []byte{0x01, 0x02, 0x03}
	enc := base64.StdEncoding.EncodeToString(raw)

	got, err := decodeB64(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 || got[0] != 0x01 {
		t.Errorf("decode mismatch: %v", got)
	}

	empty, err := decodeB64("")
	if err != nil || empty != nil {
		t.Errorf("expected nil,nil for empty input, got %v,%v", empty, err)
	}

	if _, err := decodeB64("!!!not-base64!!!"); err == nil {
		t.Errorf("expected error for invalid base64")
	}
}
