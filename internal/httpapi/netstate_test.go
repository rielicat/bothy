package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"testing"
)

// Requests read the parsed host and proxy lists while a config change makes
// another request rebuild them; under -race this reports a data race unless
// every config gets its own immutable snapshot.
func TestNetStateRebuildIsRaceFree(t *testing.T) {
	h := newHarness(t, func(c *configT) { c.Server.AllowedHosts = []string{"c.example"} })
	const rounds = 200
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range rounds {
			h.cfg.update(func(c *configT) {
				c.Server.AllowedHosts = []string{"a" + strconv.Itoa(i) + ".example", "b.example:8443", "c.example"}
				c.Server.TrustedProxies = []string{"10.0.0." + strconv.Itoa(i%250+1)}
			})
		}
	})
	for range 2 {
		wg.Go(func() {
			for range rounds {
				if !h.srv.hostAllowed("c.example") {
					t.Error("c.example refused")
					return
				}
				r := httptest.NewRequest("GET", "/", nil)
				r.RemoteAddr = "192.0.2.1:1234"
				if ip, _, _ := h.srv.peer(r); ip != netip.MustParseAddr("192.0.2.1") {
					t.Errorf("peer = %v", ip)
					return
				}
				_ = h.srv.classify(netip.MustParseAddr("10.0.0.7"), false)
			}
		})
	}
	wg.Wait()
}
