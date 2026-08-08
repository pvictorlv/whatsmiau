package controllers

import (
	"errors"
	"fmt"
	"testing"

	"github.com/verbeux-ai/whatsmiau/lib/whatsmiau"
	"go.mau.fi/whatsmeow"
)

func TestIsInstanceNotConnected(t *testing.T) {
	notConnected := []error{
		whatsmeow.ErrClientIsNil,
		whatsmiau.ErrDeviceNotConnected,
		// Wrapped along the way: callers must still get the honest status.
		fmt.Errorf("send message: %w", whatsmeow.ErrClientIsNil),
	}

	for _, err := range notConnected {
		if !isInstanceNotConnected(err) {
			t.Errorf("isInstanceNotConnected(%v) = false, want true", err)
		}
	}

	realFailures := []error{nil, errors.New("connection reset by peer")}

	for _, err := range realFailures {
		if isInstanceNotConnected(err) {
			t.Errorf("isInstanceNotConnected(%v) = true, want false", err)
		}
	}
}

func TestNumberToJidAcceptsInternationalNumbers(t *testing.T) {
	validNumbers := []string{
		"5561999211277", // Brazil (13 digits)
		"13233923870",   // United States, +1 (11 digits)
		"34662418782",   // Spain, +34 (11 digits)
	}

	for _, number := range validNumbers {
		if _, err := numberToJid(number); err != nil {
			t.Errorf("numberToJid(%q) returned unexpected error: %v", number, err)
		}
	}
}

func TestNumberToJidRoutesGroupIDsToGroupServer(t *testing.T) {
	groups := map[string]string{
		// Community id with no server: must not become a user JID.
		"120363407835046398":      "120363407835046398@g.us",
		"120363407835046398@g.us": "120363407835046398@g.us",
		// Legacy "<creator>-<timestamp>" group.
		"554288215922-1520421573": "554288215922-1520421573@g.us",
	}

	for input, want := range groups {
		jid, err := numberToJid(input)
		if err != nil {
			t.Errorf("numberToJid(%q) returned unexpected error: %v", input, err)
			continue
		}
		if jid.String() != want {
			t.Errorf("numberToJid(%q) = %q, want %q", input, jid.String(), want)
		}
	}
}

func TestNumberToJidKeepsPhoneNumbersOnUserServer(t *testing.T) {
	// 13 digits is a Brazilian phone, not a group — the length rule must not
	// swallow it.
	jid, err := numberToJid("5561999211277")
	if err != nil {
		t.Fatalf("numberToJid returned unexpected error: %v", err)
	}
	if jid.String() != "5561999211277@s.whatsapp.net" {
		t.Errorf("numberToJid = %q, want %q", jid.String(), "5561999211277@s.whatsapp.net")
	}
}

func TestNumberToJidRejectsTooShortNumbers(t *testing.T) {
	tooShort := []string{"123", "5561"}

	for _, number := range tooShort {
		if _, err := numberToJid(number); err == nil {
			t.Errorf("numberToJid(%q) expected an error, got nil", number)
		}
	}
}
