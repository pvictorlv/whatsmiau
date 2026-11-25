package whatsmiau

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestEditedContentMatchesTargetType(t *testing.T) {
	caption := proto.String("nova legenda")
	document := &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: caption}},
		},
	}
	text := &waE2E.Message{Conversation: caption}

	for messageType, want := range map[string]*waE2E.Message{
		"imageMessage":               {ImageMessage: &waE2E.ImageMessage{Caption: caption}},
		"videoMessage":               {VideoMessage: &waE2E.VideoMessage{Caption: caption}},
		"documentMessage":            document,
		"documentWithCaptionMessage": document,
		"conversation":               text,
		"extendedTextMessage":        text,
		"":                           text,
	} {
		if got := editedContent(messageType, "nova legenda"); !proto.Equal(got, want) {
			t.Errorf("editedContent(%q) = %v, want %v", messageType, got, want)
		}
	}
}
