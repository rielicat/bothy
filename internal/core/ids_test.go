package core

import (
	"crypto/sha256"
	"math/big"
	"strings"
	"testing"
	"time"
)

// crockfordBig encodes b independently of encode128: a big integer in base 32,
// left-padded to 26 symbols.
func crockfordBig(b []byte) string {
	n := new(big.Int).SetBytes(b)
	s := n.Text(32) // digits 0-9a-v
	const std = "0123456789abcdefghijklmnopqrstuv"
	out := make([]byte, len(s))
	for i := range s {
		out[i] = crockford[strings.IndexByte(std, s[i])]
	}
	return strings.Repeat("0", 26-len(out)) + string(out)
}

func TestNewID(t *testing.T) {
	prefixes := []string{PrefixUser, PrefixSpace, PrefixSession, PrefixRun, PrefixApproval, PrefixSchedule,
		PrefixWorkspace, PrefixGrant, PrefixToolCall, PrefixLLMCall, PrefixToken, PrefixBox, PrefixInstall, PrefixUpload}
	for _, p := range prefixes {
		id := NewID(p)
		if !ValidID(p, id) {
			t.Errorf("NewID(%q) = %q is not valid", p, id)
		}
		if len(id) != len(p)+1+26 {
			t.Errorf("NewID(%q) = %q has length %d", p, id, len(id))
		}
	}
	seen := map[string]bool{}
	for range 1000 {
		id := NewID(PrefixRun)
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestNewIDTimeOrdered(t *testing.T) {
	a := NewID(PrefixSession)
	time.Sleep(2 * time.Millisecond)
	b := NewID(PrefixSession)
	if a >= b {
		t.Fatalf("ids not time ordered: %q >= %q", a, b)
	}
}

func TestEncode128(t *testing.T) {
	var zero, max [16]byte
	for i := range max {
		max[i] = 0xff
	}
	ms := [16]byte{0x01, 0x8f, 0x3a, 0x7c, 0x55, 0x20, 0xde, 0xad, 0xbe, 0xef, 0, 1, 2, 3, 4, 5}
	later := ms
	later[5]++
	tests := []struct {
		name string
		in   [16]byte
		want string
	}{
		{"zero", zero, strings.Repeat("0", 26)},
		{"max", max, "7" + strings.Repeat("z", 25)},
		{"mixed", ms, crockfordBig(ms[:])},
		{"later", later, crockfordBig(later[:])},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := encode128(tt.in); got != tt.want {
				t.Fatalf("encode128 = %q, want %q", got, tt.want)
			}
		})
	}
	// The first 10 symbols hold exactly the 48-bit millisecond prefix.
	a, b := ms, ms
	b[15] ^= 0xff
	if encode128(a)[:10] != encode128(b)[:10] {
		t.Fatal("random bits leaked into the time prefix")
	}
	if encode128(ms) >= encode128(later) {
		t.Fatal("encoding does not preserve order")
	}
}

func TestDerivedID(t *testing.T) {
	sum := sha256.Sum256([]byte("session:ses_x"))
	want := "box_" + crockfordBig(sum[:16])
	if got := DerivedID(PrefixBox, "session:ses_x"); got != want {
		t.Fatalf("DerivedID = %q, want %q", got, want)
	}
	if DerivedID(PrefixBox, "session:a") == DerivedID(PrefixBox, "session:b") {
		t.Fatal("different purposes collide")
	}
	if !ValidID(PrefixBox, DerivedID(PrefixBox, "mcp:docs:usr_1")) {
		t.Fatal("derived id is not valid")
	}
}

func TestValidID(t *testing.T) {
	ok26 := "01hzx3k8m9qrstvwxyz0123456"
	tests := []struct {
		prefix, id string
		want       bool
	}{
		{"ses", "ses_" + ok26, true},
		{"ws", "ws_" + ok26, true},
		{"ses", "run_" + ok26, false},
		{"ses", "ses" + ok26, false},
		{"ses", "ses_" + ok26[:25], false},
		{"ses", "ses_" + ok26 + "0", false},
		{"ses", "ses_" + strings.ToUpper(ok26), false},
		{"ses", "ses_" + "i" + ok26[1:], false},
		{"ses", "ses_" + "l" + ok26[1:], false},
		{"ses", "ses_" + "o" + ok26[1:], false},
		{"ses", "ses_" + "u" + ok26[1:], false},
		{"ses", "ses_" + "-" + ok26[1:], false},
		{"ses", "", false},
		{"", "_" + ok26, true},
	}
	for _, tt := range tests {
		if got := ValidID(tt.prefix, tt.id); got != tt.want {
			t.Errorf("ValidID(%q, %q) = %v, want %v", tt.prefix, tt.id, got, tt.want)
		}
	}
}

func TestMicros(t *testing.T) {
	tests := []struct {
		usd  float64
		want Micros
		str  string
	}{
		{0, 0, "$0.0000"},
		{5.00, 5_000_000, "$5.0000"},
		{0.25, 250_000, "$0.2500"},
		{0.1, 100_000, "$0.1000"},
		{1.23456789, 1_234_568, "$1.2346"},
		{-1.5, -1_500_000, "$-1.5000"},
	}
	for _, tt := range tests {
		m := MicrosFromUSD(tt.usd)
		if m != tt.want {
			t.Errorf("MicrosFromUSD(%v) = %d, want %d", tt.usd, m, tt.want)
		}
		if s := m.String(); s != tt.str {
			t.Errorf("Micros(%d).String() = %q, want %q", m, s, tt.str)
		}
	}
	if got := Micros(2_500_000).USD(); got != 2.5 {
		t.Errorf("USD() = %v, want 2.5", got)
	}
}

func TestViewerCan(t *testing.T) {
	v := Viewer{User: "usr_a", Principals: []PrincipalID{"usr_a", "spc_g"}, Admin: true}
	tests := []struct {
		name string
		v    Viewer
		p    PrincipalID
		want bool
	}{
		{"own principal", v, "usr_a", true},
		{"owned space", v, "spc_g", true},
		{"foreign principal despite admin", v, "usr_b", false},
		{"empty principal", v, "", false},
		{"zero viewer", Viewer{}, "usr_a", false},
		{"system", SystemViewer(), "usr_b", true},
		{"system empty principal", SystemViewer(), "", true},
	}
	for _, tt := range tests {
		if got := tt.v.Can(tt.p); got != tt.want {
			t.Errorf("%s: Can(%q) = %v, want %v", tt.name, tt.p, got, tt.want)
		}
	}
}

func TestRoleTriggerPrincipal(t *testing.T) {
	for r, want := range map[Role]bool{RoleOwner: true, RoleAdmin: true, RoleMember: false, "": false} {
		if r.IsAdmin() != want {
			t.Errorf("Role(%q).IsAdmin() = %v", r, !want)
		}
	}
	trig := []struct {
		t                      Trigger
		interactive, scheduled bool
	}{
		{TrigMessage, true, false},
		{TrigSchedule, false, true},
		{TrigHeartbeat, false, true},
		{TrigAPI, false, false},
		{TrigResume, false, false},
	}
	for _, tt := range trig {
		if tt.t.Interactive() != tt.interactive || tt.t.Scheduled() != tt.scheduled {
			t.Errorf("Trigger(%q): Interactive=%v Scheduled=%v", tt.t, tt.t.Interactive(), tt.t.Scheduled())
		}
	}
	pr := []struct {
		p    Principal
		want bool
	}{
		{Principal{Kind: PrincipalUser, ToolProfile: "standard"}, false},
		{Principal{Kind: PrincipalUser, ToolProfile: "restricted"}, false},
		{Principal{Kind: PrincipalSpace, ToolProfile: "restricted"}, true},
		{Principal{Kind: PrincipalSpace}, true},
		{Principal{Kind: PrincipalSpace, ToolProfile: "standard"}, false},
	}
	for _, tt := range pr {
		if got := tt.p.Restricted(); got != tt.want {
			t.Errorf("%+v.Restricted() = %v, want %v", tt.p, got, tt.want)
		}
	}
	if UserPrincipal("usr_x") != "usr_x" {
		t.Error("UserPrincipal must reuse the user id")
	}
}
