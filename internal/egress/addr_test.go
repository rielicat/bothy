package egress

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
)

// ssrfHosts is the SPEC §12.2 M6 SSRF table, as URL hosts.
var ssrfHosts = []string{
	"127.0.0.1",
	"0x7f000001",
	"2130706433",
	"017700000001",
	"127.1",
	"[::1]",
	"[::ffff:127.0.0.1]",
	"[::ffff:169.254.169.254]",
	"[64:ff9b::a9fe:a9fe]",
	"[2002:a9fe:a9fe::]",
	"[2001:0:4136:e378:8000:63bf:80ff:fffe]", // Teredo, client 127.0.0.1
	"100.64.0.1",
	"198.18.0.1",
	"169.254.169.254",
	"[fd00:ec2::254]",
	"0.0.0.0",
	"10.1.2.3",
	"[fe80::1]",
	"[::]",
}

func TestSSRFTableHostsAreBlocked(t *testing.T) {
	for _, host := range ssrfHosts {
		t.Run(host, func(t *testing.T) {
			raw := strings.Trim(host, "[]")
			h, err := parseHost(raw)
			if err != nil {
				t.Fatalf("parseHost(%q): %v", raw, err)
			}
			if !h.isIP() {
				t.Fatalf("parseHost(%q) = name %q, want an IP literal", raw, h.name)
			}
			if r := addrProblem(h.ip, false); r == "" {
				t.Fatalf("%s (%s) is not blocked", raw, h.ip)
			}
		})
	}
}

func TestSSRFTableThroughWebFetchAndPublicClient(t *testing.T) {
	e := newEnv(t, nil)
	tool := &webFetch{s: e.s}
	for _, host := range ssrfHosts {
		t.Run(host, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"url": "http://" + host + "/latest/meta-data/"})
			if _, err := tool.Plan(context.Background(), &core.ToolEnv{}, args); err == nil {
				t.Fatalf("Plan accepted %s", host)
			}
			resp, err := e.s.PublicClient(e.sess, e.principal()).Get("http://" + host + "/")
			if err == nil {
				resp.Body.Close()
				t.Fatalf("PublicClient fetched %s", host)
			}
			if !errors.Is(err, errBlocked) {
				t.Fatalf("PublicClient error for %s = %v, want errBlocked", host, err)
			}
		})
	}
	if d := e.dial.calls(); len(d) != 0 {
		t.Fatalf("blocked destinations were dialed: %v", d)
	}
}

func TestNamesResolvingToBlockedAddressesAreRefused(t *testing.T) {
	e := newEnv(t, nil)
	for i, a := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "198.18.0.1", "64:ff9b::a9fe:a9fe", "::ffff:127.0.0.1"} {
		host := "evil" + string(rune('a'+i)) + ".test"
		e.res.set(host, []string{a})
		resp, err := e.s.PublicClient(e.sess, e.principal()).Get("http://" + host + "/")
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s -> %s was fetched", host, a)
		}
		if !errors.Is(err, errBlocked) {
			t.Fatalf("%s -> %s: err = %v, want errBlocked", host, a, err)
		}
	}
	// One blocked answer among public ones refuses the whole name.
	e.res.set("mixed.test", []string{publicIP, "127.0.0.1"})
	if resp, err := e.s.PublicClient(e.sess, e.principal()).Get("http://mixed.test/"); err == nil {
		resp.Body.Close()
		t.Fatal("mixed public/private answer was fetched")
	}
	if d := e.dial.calls(); len(d) != 0 {
		t.Fatalf("blocked destinations were dialed: %v", d)
	}
}

func TestAddrProblemAllowPrivate(t *testing.T) {
	cases := []struct {
		addr    string
		private bool // admitted with AllowPrivate
	}{
		{"10.0.0.1", true}, {"172.16.5.4", true}, {"192.168.1.1", true}, {"100.64.0.1", true},
		{"127.0.0.1", true}, {"169.254.1.1", true}, {"::1", true}, {"fd12::1", true}, {"fe80::1", true},
		{"169.254.169.254", false}, {"fd00:ec2::254", false}, {"100.100.100.200", false},
		{"0.0.0.0", false}, {"224.0.0.1", false}, {"255.255.255.255", false}, {"192.0.2.1", false},
		{"2001:db8::1", false}, {"ff02::1", false}, {"::", false}, {"64:ff9b::a9fe:a9fe", false},
	}
	for _, c := range cases {
		a := netip.MustParseAddr(c.addr)
		if addrProblem(a, false) == "" {
			t.Errorf("%s allowed without AllowPrivate", c.addr)
		}
		if got := addrProblem(a, true) == ""; got != c.private {
			t.Errorf("%s with AllowPrivate: allowed=%v, want %v (%s)", c.addr, got, c.private, addrProblem(a, true))
		}
	}
	for _, pub := range []string{publicIP, "1.1.1.1", "2606:4700:4700::1111", "64:ff9b::808:808", "2002:808:808::1"} {
		if r := addrProblem(netip.MustParseAddr(pub), false); r != "" {
			t.Errorf("public %s blocked: %s", pub, r)
		}
	}
}

func TestParseHost(t *testing.T) {
	ok := map[string]string{
		"Example.COM":      "example.com",
		"example.com.":     "example.com",
		"bücher.example":   "xn--bcher-kva.example",
		"аpple.com":        "xn--pple-43d.com", // Cyrillic а
		"::ffff:1.2.3.4":   "1.2.3.4",
		"[2606:4700::1]":   "2606:4700::1",
		"0x5d.0xb8.216.34": publicIP,
		"１２７.0.0.1":        "127.0.0.1", // fullwidth digits map to ASCII
	}
	for in, want := range ok {
		h, err := parseHost(in)
		if err != nil || h.name != want {
			t.Errorf("parseHost(%q) = %q, %v; want %q", in, h.name, err, want)
		}
	}
	for _, in := range []string{"", "1.2.3.4.5", "exa mple.com", "a..b", "example.123", "256.1.1.1",
		"0x100000000", "fe80::1%eth0", "*.example.com", "-bad.com", "under_score.com", strings.Repeat("a", 254)} {
		if h, err := parseHost(in); err == nil {
			t.Errorf("parseHost(%q) = %q, want an error", in, h.name)
		}
	}
}

func TestControlRechecksFinalAddress(t *testing.T) {
	ctl := controlFor(false)
	for _, a := range []string{"127.0.0.1:80", "[::1]:443", "169.254.169.254:80", "[::ffff:10.0.0.1]:443", "nonsense"} {
		if err := ctl("tcp", a, nil); !errors.Is(err, errBlocked) {
			t.Errorf("control(%s) = %v, want errBlocked", a, err)
		}
	}
	if err := ctl("tcp", publicIP+":443", nil); err != nil {
		t.Errorf("control(public) = %v", err)
	}
	if err := controlFor(true)("tcp", "10.0.0.1:8080", nil); err != nil {
		t.Errorf("control(private, allowPrivate) = %v", err)
	}
	if err := controlFor(true)("tcp", "169.254.169.254:80", nil); !errors.Is(err, errBlocked) {
		t.Errorf("metadata allowed with AllowPrivate: %v", err)
	}
}

func TestProductionDialerRefusesLoopback(t *testing.T) {
	_, err := netDial(context.Background(), "tcp", "127.0.0.1:1", false)
	if !errors.Is(err, errBlocked) {
		t.Fatalf("netDial(127.0.0.1) = %v, want errBlocked", err)
	}
}

func TestPublicClientRefusesNonGETAndCredentials(t *testing.T) {
	e := newEnv(t, nil)
	c := e.s.PublicClient(e.sess, e.principal())
	if resp, err := c.Post("http://example.com/", "text/plain", strings.NewReader("x")); err == nil {
		resp.Body.Close()
		t.Fatal("POST allowed")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://user:pw@example.com/", nil)
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("URL credentials allowed")
	}
	if len(e.dial.calls()) != 0 {
		t.Fatal("refused requests were dialed")
	}
}
