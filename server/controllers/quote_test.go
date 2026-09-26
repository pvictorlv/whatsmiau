package controllers

import (
	"testing"

	"github.com/verbeux-ai/whatsmiau/server/dto"
)

func TestBuildQuote(t *testing.T) {
	tests := []struct {
		name       string
		in         *dto.MessageRequestQuoted
		wantText   string
		wantAuthor string
	}{
		{
			name: "DM key without participant: author is the chat",
			in: &dto.MessageRequestQuoted{
				Key:     dto.QuotedKey{Id: "A1", RemoteJid: "559281358585@s.whatsapp.net"},
				Message: dto.QuotedMessage{Conversation: "oi"},
			},
			wantText: "oi", wantAuthor: "559281358585@s.whatsapp.net",
		},
		{
			name: "explicit participant wins over the chat",
			in: &dto.MessageRequestQuoted{
				Key:     dto.QuotedKey{Id: "A2", RemoteJid: "120363407835046398@g.us", Participant: "5527988593007@s.whatsapp.net"},
				Message: dto.QuotedMessage{Conversation: "no grupo"},
			},
			wantText: "no grupo", wantAuthor: "5527988593007@s.whatsapp.net",
		},
		{
			name: "own message without participant: no author guessed",
			in: &dto.MessageRequestQuoted{
				Key:     dto.QuotedKey{Id: "A3", RemoteJid: "559281358585@s.whatsapp.net", FromMe: true},
				Message: dto.QuotedMessage{Conversation: "minha"},
			},
			wantText: "minha",
		},
		{
			name: "text from extendedTextMessage",
			in: &dto.MessageRequestQuoted{
				Key:     dto.QuotedKey{Id: "A4", RemoteJid: "559281358585@s.whatsapp.net"},
				Message: dto.QuotedMessage{ExtendedTextMessage: &dto.QuotedExtendedTextMessage{Text: "com link https://x"}},
			},
			wantText: "com link https://x", wantAuthor: "559281358585@s.whatsapp.net",
		},
		{
			name: "unreadable author does not block the send",
			in: &dto.MessageRequestQuoted{
				Key:     dto.QuotedKey{Id: "A5", Participant: "@@"},
				Message: dto.QuotedMessage{Conversation: "x"},
			},
			wantText: "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := buildQuote(tt.in)
			if err != nil {
				t.Fatalf("buildQuote error: %v", err)
			}
			if q.Message != tt.wantText {
				t.Errorf("text = %q, want %q", q.Message, tt.wantText)
			}
			got := ""
			if q.Participant != nil {
				got = q.Participant.String()
			}
			if got != tt.wantAuthor {
				t.Errorf("author = %q, want %q", got, tt.wantAuthor)
			}
		})
	}
}

func TestBuildQuoteNilWithoutID(t *testing.T) {
	if q, _ := buildQuote(&dto.MessageRequestQuoted{}); q != nil {
		t.Fatalf("quote without key.id must be nil, got %+v", q)
	}
}
