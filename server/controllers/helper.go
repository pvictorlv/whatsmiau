package controllers

import (
	"fmt"
	"strings"

	"github.com/verbeux-ai/whatsmiau/lib/proxypool"
	"github.com/verbeux-ai/whatsmiau/models"
	"go.mau.fi/whatsmeow/types"
)

func numberToJid(number string) (*types.JID, error) {
	splitNumber := strings.Split(number, "@")
	if len(splitNumber) != 2 {
		number += "@s.whatsapp.net"
	}

	// E.164 numbers are 8-15 digits including the country code. The previous
	// "< 12" bound only fit Brazilian numbers (55 + DDD + 8/9 = 12-13 digits)
	// and wrongly rejected valid shorter international numbers that already
	// carry their country prefix, e.g. US +1 (11 digits) and Spain +34 (11).
	if len(splitNumber[0]) < 8 {
		return nil, fmt.Errorf("invalid jid, put country prefix")
	}

	jid, err := types.ParseJID(number)
	if err != nil {
		return nil, fmt.Errorf("invalid jid (number)")
	}

	return &jid, nil
}

func parseGroupJID(input string) (*types.JID, error) {
	if input == "" {
		return nil, fmt.Errorf("group jid is required")
	}

	if !strings.Contains(input, "@") {
		input += "@" + types.GroupServer
	}

	jid, err := types.ParseJID(input)
	if err != nil {
		return nil, fmt.Errorf("invalid group jid: %w", err)
	}

	if jid.Server != types.GroupServer {
		return nil, fmt.Errorf("not a group jid: %s", jid.String())
	}

	return &jid, nil
}

// parseProxyURL delegates to the proxy pool parser so PROXY_ADDRESSES and
// PROXY_POOL_FILE accept exactly the same spellings, including IPv6 literals.
func parseProxyURL(proxyURL string) (*models.InstanceProxy, error) {
	proxy, err := proxypool.ParseURL(proxyURL)
	if err != nil {
		return nil, err
	}

	return &proxy, nil
}
