package policy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	metaMACKey    = "approval_mac_key"
	macKeyBytes   = 32
	maxButtonData = 64 // Telegram callback_data limit
)

var (
	choiceLetter = map[core.Choice]string{core.AllowOnce: "o", core.AllowSession: "s", core.AllowAlways: "a", core.Deny: "d"}
	letterChoice = map[string]core.Choice{"o": core.AllowOnce, "s": core.AllowSession, "a": core.AllowAlways, "d": core.Deny}
	macEncoding  = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// loadMACKey reads meta approval_mac_key, creating it on first boot. A
// corrupt key is an error rather than a silent rotation, which would make
// every delivered card button fail without explanation.
func loadMACKey(ctx context.Context, meta core.MetaRepo) ([]byte, error) {
	if meta == nil {
		return nil, errors.New("policy: the store has no meta repository for the approval MAC key")
	}
	v, err := meta.Get(ctx, metaMACKey)
	switch {
	case err == nil:
		key, derr := hex.DecodeString(strings.TrimSpace(v))
		if derr != nil || len(key) != macKeyBytes {
			return nil, fmt.Errorf("policy: meta %s is corrupt (want %d hex-encoded bytes)", metaMACKey, macKeyBytes)
		}
		return key, nil
	case !errors.Is(err, core.ErrNotFound):
		return nil, fmt.Errorf("policy: read meta %s: %w", metaMACKey, err)
	}
	key := make([]byte, macKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("policy: generate approval MAC key: %w", err)
	}
	if err := meta.Set(ctx, metaMACKey, hex.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("policy: store meta %s: %w", metaMACKey, err)
	}
	return key, nil
}

// ButtonData implements core.Approvals. It returns "" for an unknown choice.
func (ap *Approvals) ButtonData(id core.ApprovalID, c core.Choice) string {
	l, ok := choiceLetter[c]
	if !ok {
		return ""
	}
	return "a:" + string(id) + ":" + l + ":" + ap.mac(id, c)
}

// ParseButtonData implements core.Approvals.
func (ap *Approvals) ParseButtonData(data string) (core.ApprovalID, core.Choice, error) {
	bad := func(why string) (core.ApprovalID, core.Choice, error) {
		return "", "", fmt.Errorf("%w: button data: %s", core.ErrInvalid, why)
	}
	if len(data) > maxButtonData {
		return bad("too long")
	}
	parts := strings.Split(data, ":")
	if len(parts) != 4 || parts[0] != "a" || !core.ValidID(core.PrefixApproval, parts[1]) {
		return bad("malformed")
	}
	c, ok := letterChoice[parts[2]]
	if !ok {
		return bad("unknown choice")
	}
	id := core.ApprovalID(parts[1])
	if !hmac.Equal([]byte(parts[3]), []byte(ap.mac(id, c))) {
		return bad("bad mac")
	}
	return id, c, nil
}

// mac is HMAC-SHA256(key, id|choice) truncated to 8 base32 characters.
func (ap *Approvals) mac(id core.ApprovalID, c core.Choice) string {
	h := hmac.New(sha256.New, ap.key)
	h.Write([]byte(string(id) + "|" + string(c)))
	return strings.ToLower(macEncoding.EncodeToString(h.Sum(nil)[:5]))
}
