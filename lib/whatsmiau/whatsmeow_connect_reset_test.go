package whatsmiau

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// Switching from QR to pairing code replaces the client of an instance while the
// observer of the old attempt is still running. These tests pin what that observer
// may do to the new client. The observer loop itself needs a live WhatsApp socket,
// so they drive its two ends: the reset that stops it and the cleanup it runs.

const resetTestID = "instance"

func newResetTestService() *Whatsmiau {
	return &Whatsmiau{
		clients:            xsync.NewMap[string, *whatsmeow.Client](),
		qrCache:            xsync.NewMap[string, string](),
		pairingCache:       xsync.NewMap[string, string](),
		observerRunning:    xsync.NewMap[string, *whatsmeow.Client](),
		observerCancel:     xsync.NewMap[string, context.CancelFunc](),
		lockConnection:     xsync.NewMap[string, *sync.Mutex](),
		connectPhoneNumber: xsync.NewMap[string, string](),
	}
}

func newResetTestClient() *whatsmeow.Client {
	return whatsmeow.NewClient(&store.Device{}, nil)
}

// startQRAttempt leaves the service as observeConnection does while waiting for a
// QR code to be scanned, and returns the context that observer would be waiting on.
func startQRAttempt(t *testing.T, s *Whatsmiau, client *whatsmeow.Client) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s.clients.Store(resetTestID, client)
	s.observerRunning.Store(resetTestID, client)
	s.observerCancel.Store(resetTestID, cancel)
	s.connectPhoneNumber.Store(resetTestID, "")
	s.qrCache.Store(resetTestID, "qr")
	return ctx
}

func TestDiscardExpiredAttemptKeepsClientOfNewerAttempt(t *testing.T) {
	s := newResetTestService()
	oldClient, newClient := newResetTestClient(), newResetTestClient()
	s.clients.Store(resetTestID, newClient)

	s.discardExpiredAttempt(resetTestID, oldClient)

	if current, ok := s.clients.Load(resetTestID); !ok || current != newClient {
		t.Fatalf("the client of the newer attempt was removed: ok=%v", ok)
	}
}

func TestDiscardExpiredAttemptRemovesItsOwnClient(t *testing.T) {
	s := newResetTestService()
	client := newResetTestClient()
	s.clients.Store(resetTestID, client)

	s.discardExpiredAttempt(resetTestID, client)

	if _, ok := s.clients.Load(resetTestID); ok {
		t.Fatal("an attempt that expired alone must not leave its client behind")
	}
}

func TestResetStopsObserverWhenMethodChanges(t *testing.T) {
	s := newResetTestService()
	observerCtx := startQRAttempt(t, s, newResetTestClient())

	s.resetIfConnectMethodChanged(context.Background(), resetTestID, "5511999999999")

	select {
	case <-observerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("the observer of the old attempt is still running after the reset")
	}
	if _, ok := s.clients.Load(resetTestID); ok {
		t.Fatal("the old client should be gone after the reset")
	}
	if _, ok := s.observerRunning.Load(resetTestID); ok {
		t.Fatal("the observer marker should be gone after the reset")
	}
	if _, ok := s.observerCancel.Load(resetTestID); ok {
		t.Fatal("the cancel of the old attempt should be gone after the reset")
	}
	if _, ok := s.qrCache.Load(resetTestID); ok {
		t.Fatal("the QR code of the old attempt should be gone after the reset")
	}
}

func TestResetKeepsObserverWhenMethodIsTheSame(t *testing.T) {
	s := newResetTestService()
	client := newResetTestClient()
	observerCtx := startQRAttempt(t, s, client)

	s.resetIfConnectMethodChanged(context.Background(), resetTestID, "")

	if err := observerCtx.Err(); err != nil {
		t.Fatalf("the observer was stopped without a method change: %v", err)
	}
	if current, ok := s.clients.Load(resetTestID); !ok || current != client {
		t.Fatal("the client should stay when the method did not change")
	}
}
