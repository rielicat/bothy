package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestCanonicalJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    string
		wantErr bool
	}{
		{"sorted nested keys", json.RawMessage(`{"z":1,"a":{"y":2,"b":3}}`), `{"a":{"b":3,"y":2},"z":1}`, false},
		{"whitespace removed", json.RawMessage(" {\n \"a\" : [ 1 , 2 ] \t}\n"), `{"a":[1,2]}`, false},
		{"array order kept", json.RawMessage(`[3,1,2]`), `[3,1,2]`, false},
		{"numbers verbatim", json.RawMessage(`{"a":1.50,"b":1e3,"c":-0,"d":12345678901234567890}`),
			`{"a":1.50,"b":1e3,"c":-0,"d":12345678901234567890}`, false},
		{"no html escaping", json.RawMessage(`{"h":"<b>&</b>"}`), `{"h":"<b>&</b>"}`, false},
		{"escapes normalised", json.RawMessage(`{"s":"A\/"}`), `{"s":"A/"}`, false},
		{"unicode kept", json.RawMessage(`{"s":"héllo ✓"}`), `{"s":"héllo ✓"}`, false},
		{"scalars", json.RawMessage(`"x"`), `"x"`, false},
		{"null", json.RawMessage(`null`), `null`, false},
		{"bytes input", []byte(`{"b":true,"a":null}`), `{"a":null,"b":true}`, false},
		{"struct input", struct {
			Z string `json:"z"`
			A []int  `json:"a"`
		}{"<x>", []int{2, 1}}, `{"a":[2,1],"z":"<x>"}`, false},
		{"map input", map[string]any{"b": 1, "a": "2"}, `{"a":"2","b":1}`, false},
		{"invalid json", json.RawMessage(`{"a":`), "", true},
		{"empty input", json.RawMessage(``), "", true},
		{"unmarshalable value", make(chan int), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalJSON(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
			if err == nil {
				again, err := CanonicalJSON(json.RawMessage(got))
				if err != nil || string(again) != string(got) {
					t.Fatalf("not idempotent: %s -> %s (%v)", got, again, err)
				}
			}
		})
	}
}

func TestActionFingerprint(t *testing.T) {
	payload := []byte(`{"argv":["bash","-lc","ls"],"cwd":""}`)
	sum := sha256.Sum256(append([]byte("shell\x00"), payload...))
	if got, want := ActionFingerprint("shell", payload), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("ActionFingerprint = %s, want %s", got, want)
	}
	if ActionFingerprint("ab", []byte("c")) == ActionFingerprint("a", []byte("bc")) {
		t.Fatal("tool/payload boundary is ambiguous")
	}
	if len(ActionFingerprint("", nil)) != 64 {
		t.Fatal("fingerprint is not hex sha256")
	}
}

func TestSideEffectText(t *testing.T) {
	names := map[SideEffect]string{
		SERead: "read", SELocalWrite: "local_write", SEExternalSend: "external_send",
		SEIrreversible: "irreversible", SEFinancial: "financial", SEAdmin: "admin",
	}
	for se, name := range names {
		if se.String() != name || !se.Valid() {
			t.Errorf("SideEffect(%d): String=%q Valid=%v", se, se.String(), se.Valid())
		}
		b, err := se.MarshalText()
		if err != nil || string(b) != name {
			t.Errorf("MarshalText(%d) = %q, %v", se, b, err)
		}
		var back SideEffect
		if err := back.UnmarshalText([]byte(name)); err != nil || back != se {
			t.Errorf("UnmarshalText(%q) = %d, %v", name, back, err)
		}
		if p, err := ParseSideEffect(name); err != nil || p != se {
			t.Errorf("ParseSideEffect(%q) = %d, %v", name, p, err)
		}
	}
	for _, bad := range []SideEffect{0, SEAdmin + 1, 255} {
		if bad.Valid() {
			t.Errorf("SideEffect(%d) reported valid", bad)
		}
		if _, err := bad.MarshalText(); err == nil {
			t.Errorf("MarshalText(%d) succeeded", bad)
		}
	}
	if s := SideEffect(9).String(); s != "side_effect(9)" {
		t.Errorf("String() of out-of-range = %q", s)
	}
	for _, bad := range []string{"", "Read", "write", "side_effect(1)"} {
		var se SideEffect = SEAdmin
		if err := se.UnmarshalText([]byte(bad)); err == nil || se != 0 {
			t.Errorf("UnmarshalText(%q) = %d, %v; want 0 and an error", bad, se, err)
		}
	}
}

func TestSideEffectJSON(t *testing.T) {
	b, err := json.Marshal(Manifest{Title: "Run shell command", SideEffect: SEExternalSend})
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil || m.SideEffect != SEExternalSend {
		t.Fatalf("round trip = %d, %v (%s)", m.SideEffect, err, b)
	}
	if _, err := json.Marshal(Manifest{Title: "missing class"}); err == nil {
		t.Fatal("zero side effect marshalled")
	}
	if err := json.Unmarshal([]byte(`{"side_effect":"nope"}`), &m); err == nil {
		t.Fatal("unknown side effect unmarshalled")
	}
}

func TestTaint(t *testing.T) {
	tests := []struct {
		t       Taint
		exposed bool
	}{
		{0, false},
		{TaintPrivate, false},
		{TaintUntrusted, false},
		{TaintSend, false},
		{TaintPrivate | TaintSend, false},
		{TaintUntrusted | TaintSend, false},
		{TaintPrivate | TaintUntrusted, true},
		{TaintPrivate | TaintUntrusted | TaintSend, true},
	}
	for _, tt := range tests {
		if got := tt.t.Exposed(); got != tt.exposed {
			t.Errorf("Taint(%03b).Exposed() = %v, want %v", tt.t, got, tt.exposed)
		}
	}
	all := TaintPrivate | TaintUntrusted | TaintSend
	has := []struct {
		t, x Taint
		want bool
	}{
		{all, TaintPrivate, true},
		{all, TaintPrivate | TaintUntrusted, true},
		{TaintPrivate, TaintPrivate | TaintUntrusted, false},
		{TaintUntrusted, TaintPrivate, false},
		{0, 0, true},
	}
	for _, tt := range has {
		if got := tt.t.Has(tt.x); got != tt.want {
			t.Errorf("Taint(%03b).Has(%03b) = %v, want %v", tt.t, tt.x, got, tt.want)
		}
	}
}
