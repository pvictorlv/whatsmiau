package whatsmiau

import (
	"errors"
	"fmt"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestIsMediaGoneOnlyMatchesCDNExpiry(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"410", whatsmeow.ErrMediaDownloadFailedWith410, true},
		{"404", whatsmeow.ErrMediaDownloadFailedWith404, true},
		{"403", whatsmeow.ErrMediaDownloadFailedWith403, true},
		{"wrapped 410", fmt.Errorf("download failed: %w", whatsmeow.ErrMediaDownloadFailedWith410), true},
		{"bad hash", whatsmeow.ErrInvalidMediaSHA256, false},
		{"network", errors.New("connection reset"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMediaGone(tc.err); got != tc.want {
				t.Errorf("isMediaGone(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDeliverMediaRetryReachesTheWaiter(t *testing.T) {
	const instanceID = "instance-1"
	const messageID = "3EB0ABC"

	waiter := make(chan *events.MediaRetry, 1)
	key := mediaRetryKey(instanceID, messageID)
	mediaRetryWaiters.Store(key, waiter)
	defer mediaRetryWaiters.Delete(key)

	deliverMediaRetry(instanceID, &events.MediaRetry{
		MessageID: types.MessageID(messageID),
		ChatID:    types.NewJID("5511999999999", types.DefaultUserServer),
	})

	select {
	case got := <-waiter:
		if string(got.MessageID) != messageID {
			t.Errorf("unexpected message id: %q", got.MessageID)
		}
	default:
		t.Fatal("expected the notification to reach the waiter")
	}
}

// Uma notificação de outra instância não pode acordar quem espera nesta: os
// IDs de mensagem do WhatsApp não são únicos entre contas.
func TestDeliverMediaRetryIsScopedByInstance(t *testing.T) {
	const messageID = "3EB0ABC"

	waiter := make(chan *events.MediaRetry, 1)
	key := mediaRetryKey("instance-1", messageID)
	mediaRetryWaiters.Store(key, waiter)
	defer mediaRetryWaiters.Delete(key)

	deliverMediaRetry("instance-2", &events.MediaRetry{MessageID: types.MessageID(messageID)})

	select {
	case got := <-waiter:
		t.Fatalf("expected no delivery across instances, got %+v", got)
	default:
	}
}

func TestDeliverMediaRetryWithoutWaiterDoesNotBlock(t *testing.T) {
	deliverMediaRetry("instance-1", &events.MediaRetry{MessageID: "unrequested"})
}

func TestRequestMediaReuploadRejectsUnusableTargets(t *testing.T) {
	s := &Whatsmiau{}
	chat := types.NewJID("5511999999999", types.DefaultUserServer)

	cases := []struct {
		name     string
		target   *MediaRetryTarget
		mediaKey []byte
	}{
		{"nil target", nil, []byte("key")},
		{"no message id", &MediaRetryTarget{Chat: chat}, []byte("key")},
		{"no media key", &MediaRetryTarget{MessageID: "3EB0ABC", Chat: chat}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.requestMediaReupload(t.Context(), "instance-1", nil, tc.target, tc.mediaKey)
			if !errors.Is(err, ErrMediaGone) {
				t.Errorf("expected ErrMediaGone, got %v", err)
			}
		})
	}
}
