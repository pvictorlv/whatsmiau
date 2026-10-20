package whatsmiau

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func mustJID(t *testing.T, s string) types.JID {
	t.Helper()
	jid, err := types.ParseJID(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return jid
}

func TestUsesLIDAddressing(t *testing.T) {
	if usesLIDAddressing(nil) {
		t.Fatal("nil group info must not count as LID")
	}
	if usesLIDAddressing(&types.GroupInfo{AddressingMode: types.AddressingModePN}) {
		t.Fatal("a phone-addressed group must not count as LID")
	}
	if !usesLIDAddressing(&types.GroupInfo{AddressingMode: types.AddressingModeLID}) {
		t.Fatal("an LID-addressed group must count as LID")
	}
}

func TestQuoteForChatLeavesQuotesThatNeedNoChange(t *testing.T) {
	s := &Whatsmiau{}
	group := mustJID(t, "120363000000000000@g.us")
	direct := mustJID(t, "5511999999999@s.whatsapp.net")
	lid := mustJID(t, "247695209967637@lid")

	cases := map[string]struct {
		chat types.JID
		q    *Quote
	}{
		"no quote":            {chat: group, q: nil},
		"no participant":      {chat: group, q: &Quote{MessageID: "A"}},
		"direct chat":         {chat: direct, q: &Quote{MessageID: "A", Participant: &direct}},
		"author already LID":  {chat: group, q: &Quote{MessageID: "A", Participant: &lid}},
		"phone author, group": {chat: group, q: &Quote{MessageID: "A", Participant: &direct}},
	}

	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			// A nil client is the "no way to look anything up" case: the quote must
			// come back exactly as it went in.
			if got := s.quoteForChat(context.Background(), nil, tt.chat, tt.q); got != tt.q {
				t.Fatalf("expected the same quote back, got %+v", got)
			}
		})
	}
}
