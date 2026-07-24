package controllers

import (
	"encoding/base64"
	"testing"

	"github.com/verbeux-ai/whatsmiau/server/dto"
	"go.mau.fi/whatsmeow"
)

func TestSelectMediaLocatorPicksCorrectType(t *testing.T) {
	cases := []struct {
		name     string
		msg      dto.GetBase64Message
		wantType whatsmeow.MediaType
		wantNil  bool
	}{
		{"image", dto.GetBase64Message{ImageMessage: &dto.MediaLocator{Mimetype: "image/jpeg"}}, whatsmeow.MediaImage, false},
		{"sticker", dto.GetBase64Message{StickerMessage: &dto.MediaLocator{}}, whatsmeow.MediaImage, false},
		{"video", dto.GetBase64Message{VideoMessage: &dto.MediaLocator{}}, whatsmeow.MediaVideo, false},
		{"ptv", dto.GetBase64Message{PtvMessage: &dto.MediaLocator{}}, whatsmeow.MediaVideo, false},
		{"audio", dto.GetBase64Message{AudioMessage: &dto.MediaLocator{}}, whatsmeow.MediaAudio, false},
		{"document", dto.GetBase64Message{DocumentMessage: &dto.MediaLocator{}}, whatsmeow.MediaDocument, false},
		{"empty", dto.GetBase64Message{}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locator, mediaType, _ := selectMediaLocator(&tc.msg)
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
