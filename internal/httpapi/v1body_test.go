package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTopLevelKeys(t *testing.T) {
	body := []byte(` {"model":"bothy","messages":[{"role":"user","content":"a}\"]{,"}],` +
		`"logprobs":true, "tools" : null,"n":2,"x":{"y":[1,{"z":"}"}]},"model2":-1.5e3} `)
	keys, err := topLevelKeys(body)
	if err != nil {
		t.Fatal(err)
	}
	want := v1Keys{"model": `"`, "messages": "[", "logprobs": "true", "tools": "null", "n": "2", "x": "{", "model2": "-1.5e3"}
	if !maps.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if keys.set("tools") || !keys.set("n") || keys.set("absent") {
		t.Fatal("set() must mean present and not null")
	}
	if got := keys.ignored([]string{"model", "messages"}); strings.Join(got, ",") != "logprobs,model2,n,tools,x" {
		t.Fatalf("ignored = %v", got)
	}
	for _, bad := range []string{`[1]`, `"x"`, ``, `{"a":`} {
		if _, err := topLevelKeys([]byte(bad)); err == nil {
			t.Errorf("topLevelKeys(%q) accepted a non-object", bad)
		}
	}
}

// One /v1 request with images already outweighs the gateway's memory limit,
// so large bodies take a slot and a request that cannot get one in time is
// turned away; small requests never wait.
func TestV1LargeBodiesTakeASlot(t *testing.T) {
	h := newHarness(t).setup()
	h.useScriptRunner(defaultScript)
	tok := h.tokenFor(h.member, "chat")
	h.srv.v1Wait = 20 * time.Millisecond
	big, err := json.Marshal(map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": strings.Repeat("x", 2<<20)}}})
	if err != nil {
		t.Fatal(err)
	}
	small := map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	chunked := func(b []byte) reqOpt {
		return func(r *http.Request) {
			r.Body, r.ContentLength = io.NopCloser(struct{ io.Reader }{bytes.NewReader(b)}), -1
		}
	}
	turnedAway := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		expect(t, rec, http.StatusTooManyRequests)
		if e := decodeAs[oaError](t, rec).Error; e.Code != "rate_limited" || e.Type != "rate_limit_error" || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("busy answer = %+v, Retry-After %q", e, rec.Header().Get("Retry-After"))
		}
	}

	h.srv.v1Large <- struct{}{} // another large body is being parsed
	turnedAway(h.do("POST", "/v1/chat/completions", big, tok))
	turnedAway(h.do("POST", "/v1/chat/completions", "", tok, chunked(big)))
	expect(t, h.do("POST", "/v1/chat/completions", small, tok), http.StatusOK)
	smallJSON, _ := json.Marshal(small)
	expect(t, h.do("POST", "/v1/chat/completions", "", tok, chunked(smallJSON)), http.StatusOK)
	<-h.srv.v1Large

	expect(t, h.do("POST", "/v1/chat/completions", big, tok), http.StatusOK)
	expect(t, h.do("POST", "/v1/chat/completions", "", tok, chunked(big)), http.StatusOK)
	if n := len(h.srv.v1Large); n != 0 {
		t.Fatalf("%d large-body slots still held after the requests ended", n)
	}
}
