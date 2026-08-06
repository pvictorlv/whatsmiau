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

func TestNumberToJidRejectsTooShortNumbers(t *testing.T) {
	tooShort := []string{"123", "5561"}

	for _, number := range tooShort {
		if _, err := numberToJid(number); err == nil {
			t.Errorf("numberToJid(%q) expected an error, got nil", number)
		}
	}
}
