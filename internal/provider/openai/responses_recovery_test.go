package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

// A reply cut off at the output limit keeps its reasoning item but loses the
// call that followed it. Sent back alone, the reasoning item would make the
// API reject every later call of the session.
func TestResponsesDropsReasoningWithoutFollowingItem(t *testing.T) {
	cookbook := string(wiretest.File(t, "testdata/cookbook_reasoning_tool.sse"))
	var head strings.Builder
	for _, ev := range strings.Split(cookbook, "\n\n") {
		if strings.Contains(ev, `"output_index":0`) || strings.Contains(ev, "response.output_item.added") && strings.Contains(ev, `"output_index":1`) ||
			strings.Contains(ev, "function_call_arguments.delta") && strings.Contains(ev, "latitude") {
			head.WriteString(ev + "\n\n")
		}
	}
	resp, ev, _, err := respStream(t, head.String()+event(t, wiretest.File(t, "testdata/spec_incomplete.json")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != core.StopMaxTokens || len(ev.Done()) != 0 || len(resp.Message.Parts) != 1 || resp.Message.Parts[0].Type != core.PartThinking || resp.Message.Parts[0].Sig == "" {
		t.Fatalf("resp %+v", resp)
	}
	c := NewResponses(respOpts("openai"), nil)
	b, _ := json.Marshal(c.buildRequest(&core.Request{Messages: []core.Message{userText("weather?"), resp.Message, userText("go on")}}).Input)
	if strings.Contains(string(b), "reasoning") || strings.Contains(string(b), "rs_") {
		t.Fatalf("orphaned reasoning replayed: %s", b)
	}

	// Only the trailing reasoning goes: the one before the calls stays.
	turn := assistantTurn("openai")
	turn.Parts = append(turn.Parts, core.Part{Type: core.PartThinking, Sig: `openai:{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"gAAAA2"}`, SigKind: SigResponses})
	b, _ = json.Marshal(c.buildRequest(&core.Request{Messages: []core.Message{turn, toolResults("call_a", "call_b")}}).Input)
	if !strings.Contains(string(b), `"id":"rs_1"`) || strings.Contains(string(b), "rs_2") || !strings.Contains(string(b), `"id":"fc_b"`) {
		t.Fatalf("input %s", b)
	}
}

type respBody struct {
	Include []string          `json:"include"`
	Input   []json.RawMessage `json:"input"`
}

func decodeBody(t *testing.T, raw []byte) (respBody, string) {
	t.Helper()
	var b respBody
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b, string(raw)
}

const staleBody = `{"error":{"message":"The encrypted content for item rs_1 could not be verified. Reason: Encrypted content organization_id did not match the target organization.","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`

// Encrypted reasoning is bound to the organization that wrote it: after the
// provider's key moves to another organization, every request replaying it
// is refused. The client sends the request once more without the old
// reasoning and stops replaying it, while newer reasoning still goes back.
func TestResponsesStaleEncryptedReasoning(t *testing.T) {
	ok := wiretest.SSE(wiretest.File(t, "testdata/spec_stream.sse"))
	srv := wiretest.NewServer(t, wiretest.JSON(http.StatusBadRequest, []byte(staleBody)), ok)
	c := NewResponses(wiretest.Options(srv, "openai"), nil)
	old := assistantTurn("openai")
	history := []core.Message{userText("Compare a and b."), old, toolResults("call_a", "call_b")}
	var ev wiretest.Events
	resp, err := c.Stream(context.Background(), &core.Request{Model: "gpt-6-astra", Messages: history}, ev.On)
	if err != nil || resp.Message.PlainText() == "" {
		t.Fatalf("%v %+v", err, resp)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %d", len(reqs))
	}
	first, raw1 := decodeBody(t, reqs[0].Body)
	second, raw2 := decodeBody(t, reqs[1].Body)
	if !strings.Contains(raw1, `"rs_1"`) || strings.Contains(raw2, "reasoning\"") || strings.Contains(raw2, `"id":`) ||
		len(first.Include) != 1 || len(second.Include) != 1 || len(second.Input) != len(first.Input)-1 {
		t.Fatalf("first %s\nsecond %s", raw1, raw2)
	}

	// Later calls leave the old reasoning out at once; a newer reply's
	// reasoning is replayed.
	newer := assistantTurn("openai")
	newer.CreatedAt = time.Now().Add(time.Minute)
	newer.Parts[0].Sig = `openai:{"type":"reasoning","id":"rs_9","summary":[],"encrypted_content":"gAAAA9"}`
	history = append(history, newer, toolResults("call_a", "call_b"))
	if _, err := c.Stream(context.Background(), &core.Request{Model: "gpt-6-astra", Messages: history}, nil); err != nil {
		t.Fatal(err)
	}
	reqs = srv.Requests()
	_, raw3 := decodeBody(t, reqs[2].Body)
	if len(reqs) != 3 || strings.Contains(raw3, `"rs_1"`) || !strings.Contains(raw3, `"rs_9"`) || !strings.Contains(raw3, `"id":"fc_a"`) {
		t.Fatalf("requests %d, third %s", len(reqs), raw3)
	}
}

// A model the catalog does not know (a fine-tune of a model that does not
// reason) refuses the include; it is sent once more without it, and later
// requests for that model leave it out.
func TestResponsesModelWithoutEncryptedContent(t *testing.T) {
	refused := []byte(`{"error":{"message":"Encrypted content is not supported with this model.","type":"invalid_request_error","param":"include","code":null}}`)
	ok := wiretest.SSE(wiretest.File(t, "testdata/spec_stream.sse"))
	srv := wiretest.NewServer(t, wiretest.JSON(http.StatusBadRequest, refused), ok)
	c := NewResponses(wiretest.Options(srv, "openai"), nil)
	const ft = "ft:gpt-4.1-mini-2025-04-14:acme::abc123"
	for range 2 {
		if _, err := c.Stream(context.Background(), &core.Request{Model: ft, Messages: []core.Message{userText("hi")}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Stream(context.Background(), &core.Request{Model: "gpt-6-astra", Messages: []core.Message{userText("hi")}}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 4 {
		t.Fatalf("requests %d", len(reqs))
	}
	for i, want := range []int{1, 0, 0, 1} {
		if b, raw := decodeBody(t, reqs[i].Body); len(b.Include) != want {
			t.Fatalf("request %d: %s", i, raw)
		}
	}
}

const tempRefused = `{"error":{"message":"Unsupported parameter: 'temperature' is not supported with this model.","type":"invalid_request_error","param":"temperature","code":"unsupported_parameter"}}`

// A reasoning model the catalog does not know refuses the temperature a /v1
// client set: the request goes once more without it, and later requests for
// that model leave it out at once while other models keep theirs.
func TestResponsesTemperatureRefused(t *testing.T) {
	ok := wiretest.SSE(wiretest.File(t, "testdata/spec_stream.sse"))
	srv := wiretest.NewServer(t, wiretest.JSON(http.StatusBadRequest, []byte(tempRefused)), ok)
	c := NewResponses(wiretest.Options(srv, "openai"), nil)
	temp := 0.2
	for _, model := range []string{"ft:o9-mini:acme::x1", "ft:o9-mini:acme::x1", "gpt-4.1"} {
		if _, err := c.Stream(context.Background(), &core.Request{Model: model, Temperature: &temp, Messages: []core.Message{userText("hi")}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	reqs := srv.Requests()
	if len(reqs) != 4 {
		t.Fatalf("requests %d", len(reqs))
	}
	for i, want := range []bool{true, false, false, true} {
		if got := strings.Contains(string(reqs[i].Body), `"temperature":0.2`); got != want {
			t.Fatalf("request %d temperature %v: %s", i, got, reqs[i].Body)
		}
	}

	// Without a temperature to drop, the same refusal is final.
	srv = wiretest.NewServer(t, wiretest.JSON(http.StatusBadRequest, []byte(tempRefused)), ok)
	if _, err := NewResponses(wiretest.Options(srv, "openai"), nil).Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil); err == nil || len(srv.Requests()) != 1 {
		t.Fatalf("%v, %d requests", err, len(srv.Requests()))
	}
}

// A model can refuse the temperature and then the encrypted reasoning; each
// downgrade happens once, and the call still succeeds.
func TestResponsesStackedDowngrades(t *testing.T) {
	refused := []byte(`{"error":{"message":"Encrypted content is not supported with this model.","type":"invalid_request_error","param":"include","code":null}}`)
	ok := wiretest.SSE(wiretest.File(t, "testdata/spec_stream.sse"))
	srv := wiretest.NewServer(t, wiretest.JSON(http.StatusBadRequest, []byte(tempRefused)), wiretest.JSON(http.StatusBadRequest, refused), ok)
	temp := 1.0
	if _, err := NewResponses(wiretest.Options(srv, "openai"), nil).Stream(context.Background(), &core.Request{Model: "m", Temperature: &temp, Messages: []core.Message{userText("hi")}}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 3 {
		t.Fatalf("requests %d", len(reqs))
	}
	last, raw := decodeBody(t, reqs[2].Body)
	if len(last.Include) != 0 || strings.Contains(raw, "temperature") {
		t.Fatalf("last request %s", raw)
	}
}

func TestTemperatureRefusalMatching(t *testing.T) {
	for msg, want := range map[string]bool{
		"Unsupported parameter: 'temperature' is not supported with this model.":                                          true,
		"Unsupported value: 'temperature' does not support 0.2 with this model. Only the default (1) value is supported.": true,
		"Invalid type for 'temperature': expected a decimal, but got a string instead.":                                   false,
		"Unsupported parameter: 'max_tokens' is not supported with this model.":                                           false,
	} {
		err := &core.ProviderError{Kind: core.ErrKindInvalidRequest, Msg: msg}
		if got := temperatureRefusal(err); got != want {
			t.Errorf("%q: %v", msg, got)
		}
	}
	if temperatureRefusal(&core.ProviderError{Kind: core.ErrKindServer, Msg: "temperature not supported"}) {
		t.Error("a server error matched")
	}
}

func TestResponsesTemperatureMemoryBounded(t *testing.T) {
	c := NewResponses(respOpts("openai"), nil)
	for i := range maxPlainModels + 5 {
		c.markNoTemp(fmt.Sprintf("m%d", i))
	}
	c.mu.Lock()
	n := len(c.noTemp)
	c.mu.Unlock()
	if n > maxPlainModels || n == 0 {
		t.Fatalf("remembered %d models", n)
	}
}

func TestResponsesEncryptedRefusalNotRetried(t *testing.T) {
	ok := wiretest.SSE(wiretest.File(t, "testdata/spec_stream.sse"))
	history := []core.Message{userText("Compare a and b."), assistantTurn("openai"), toolResults("call_a", "call_b")}
	for name, tc := range map[string]struct {
		first    wiretest.Response
		messages []core.Message
	}{
		// Nothing to leave out: the same request would fail the same way.
		"stale without reasoning": {wiretest.JSON(http.StatusBadRequest, []byte(staleBody)), []core.Message{userText("hi")}},
		"unrelated 400":           {wiretest.JSON(http.StatusBadRequest, []byte(`{"error":{"message":"Invalid schema for function 'fs_read': schema must be a JSON Schema of 'type: \"object\"'.","type":"invalid_request_error","param":"tools[0].parameters","code":"invalid_function_parameters"}}`)), history},
		// After the first event nothing is sent again.
		"in-stream after text": {wiretest.SSE([]byte(strings.Split(string(wiretest.File(t, "testdata/spec_stream.sse")), ": Intermediate")[0] +
			`data: {"type":"error","code":"invalid_encrypted_content","message":"The encrypted content for item rs_1 could not be verified.","param":null,"sequence_number":5}` + "\n\n")), history},
	} {
		srv := wiretest.NewServer(t, tc.first, ok)
		_, err := NewResponses(wiretest.Options(srv, "openai"), nil).Stream(context.Background(), &core.Request{Model: "gpt-6-astra", Messages: tc.messages}, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || len(srv.Requests()) != 1 {
			t.Fatalf("%s: %v, %d requests", name, err, len(srv.Requests()))
		}
	}
}

// One client serves every call of a provider at once; what it learns from a
// refusal is shared state.
func TestResponsesConcurrentRefusals(t *testing.T) {
	ok := wiretest.SSE(wiretest.File(t, "testdata/spec_stream.sse"))
	srv := wiretest.NewServer(t, wiretest.JSON(http.StatusBadRequest, []byte(staleBody)), ok)
	c := NewResponses(wiretest.Options(srv, "openai"), nil)
	history := []core.Message{userText("Compare a and b."), assistantTurn("openai"), toolResults("call_a", "call_b")}
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := c.Stream(context.Background(), &core.Request{Model: "gpt-6-astra", Messages: history}, nil)
			errs <- err
		}()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := len(srv.Requests()); n < 9 || n > 16 {
		t.Fatalf("requests %d", n)
	}
}
