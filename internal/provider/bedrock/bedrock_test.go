package bedrock

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

func TestStreamText(t *testing.T) {
	resp, ev, _, err := streamFrames(t, readFrames(t, "live_text.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := core.Usage{Input: 23, Output: 8}
	if ev.Text() != "Hello from Bedrock" || resp.Message.PlainText() != "Hello from Bedrock" {
		t.Fatalf("text %q / %q", ev.Text(), resp.Message.PlainText())
	}
	if resp.Usage != want || resp.UsageEstimated || resp.Stop != core.StopEnd || resp.Model != "us.anthropic.claude-sonnet-4-5-20250929-v1:0" {
		t.Fatalf("resp %+v", resp)
	}
	if last := ev.All[len(ev.All)-1]; last.Type != core.StreamUsage || *last.Usage != want {
		t.Fatalf("last event %+v", last)
	}
}

// The raw recording exercises the decoder on Bedrock's own framing and CRCs.
func TestStreamRawRecording(t *testing.T) {
	srv := wiretest.NewServer(t, eventStream(wiretest.File(t, "testdata/live_text.eventstream")))
	c, _ := testClient(srv)
	resp, err := c.Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.PlainText() != "Hello from Bedrock" || resp.Usage.Output != 8 {
		t.Fatalf("resp %+v", resp)
	}
}

func TestStreamParallelToolUse(t *testing.T) {
	resp, ev, _, err := streamFrames(t, readFrames(t, "live_parallel_tools.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 2 || done[0].ID != "tooluse_z9kWbOqEJlsLF51YDXODef" || done[0].Name != "get_weather" ||
		done[1].ID != "tooluse_cSWxupnlwDuvLwIhi3eQGL" || done[1].Name != "get_time" {
		t.Fatalf("calls %+v", done)
	}
	for _, d := range done {
		if string(d.Args) != `{"city":"Paris"}` {
			t.Fatalf("args %s", d.Args)
		}
	}
	if resp.Stop != core.StopTool || len(resp.Message.Parts) != 2 || resp.Usage != (core.Usage{Input: 634, Output: 89}) {
		t.Fatalf("resp %+v", resp)
	}
	var starts []string
	for _, e := range ev.All {
		if e.Type == core.StreamToolCallStart {
			if e.Call.Args != nil {
				t.Fatalf("start carries args %s", e.Call.Args)
			}
			starts = append(starts, e.Call.ID)
		}
	}
	if !slices.Equal(starts, []string{"tooluse_z9kWbOqEJlsLF51YDXODef", "tooluse_cSWxupnlwDuvLwIhi3eQGL"}) {
		t.Fatalf("starts %v", starts)
	}
}

// reasoningOf joins what a recording streamed as reasoning text and
// signature, so the assertions do not restate the fixture.
func reasoningOf(t *testing.T, frames []frame) (text, sig string) {
	t.Helper()
	for _, f := range frames {
		var p struct {
			Delta struct {
				ReasoningContent struct {
					Text      string `json:"text"`
					Signature string `json:"signature"`
				} `json:"reasoningContent"`
			} `json:"delta"`
		}
		if f.Headers[":event-type"] != "contentBlockDelta" {
			continue
		}
		if err := json.Unmarshal([]byte(f.Payload), &p); err != nil {
			t.Fatal(err)
		}
		text += p.Delta.ReasoningContent.Text
		sig += p.Delta.ReasoningContent.Signature
	}
	return text, sig
}

func TestStreamReasoningSignatureRoundTrip(t *testing.T) {
	frames := readFrames(t, "live_reasoning_tool.jsonl")
	wantText, wantSig := reasoningOf(t, frames)
	if !strings.HasPrefix(wantText, "The user wants to know if they should take an umbrella in Oslo") || !strings.HasPrefix(wantSig, "EqoDCn4IEhAB") {
		t.Fatalf("fixture reasoning %q sig %q", wantText, wantSig)
	}
	resp, ev, _, err := streamFrames(t, frames, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Thinking() != wantText {
		t.Fatalf("thinking deltas %q", ev.Thinking())
	}
	p := resp.Message.Parts
	if len(p) != 3 || p[0].Type != core.PartThinking || p[1].Type != core.PartText || p[2].Type != core.PartToolCall {
		t.Fatalf("parts %+v", p)
	}
	if p[0].Text != wantText || p[0].Sig != wantSig || p[0].SigKind != core.KindBedrock {
		t.Fatalf("thinking part %+v", p[0])
	}
	if p[1].Text != "I'll check the weather in Oslo for you." || p[2].ToolCall.ID != "tooluse_k3cMZ8sgoQYYQEnRl1CEdk" ||
		string(p[2].ToolCall.Args) != `{"city":"Oslo"}` || resp.Stop != core.StopTool {
		t.Fatalf("parts %+v stop %q", p, resp.Stop)
	}

	// Round trip: the signed block goes back first and verbatim, the tool
	// result answers the call in a user message.
	req := buildRequest(&core.Request{Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Tools: []core.ToolDef{{Name: "get_weather", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []core.Message{userText("Should I take an umbrella in Oslo today?"), resp.Message,
			{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{
				CallID: "tooluse_k3cMZ8sgoQYYQEnRl1CEdk", Name: "get_weather", Content: `{"forecast":"rain"}`}}}}}})
	got, _ := json.Marshal(req.Messages[1:])
	want := `[{"role":"assistant","content":[{"reasoningContent":{"reasoningText":{"text":` + jsonString(wantText) + `,"signature":` + jsonString(wantSig) + `}}},` +
		`{"text":"I'll check the weather in Oslo for you."},` +
		`{"toolUse":{"toolUseId":"tooluse_k3cMZ8sgoQYYQEnRl1CEdk","name":"get_weather","input":{"city":"Oslo"}}}]},` +
		`{"role":"user","content":[{"toolResult":{"toolUseId":"tooluse_k3cMZ8sgoQYYQEnRl1CEdk","content":[{"text":"{\"forecast\":\"rain\"}"}]}}]}]`
	if string(got) != want {
		t.Fatalf("round trip\n got %s\nwant %s", got, want)
	}

	// Another kind's reasoning never reaches Bedrock, and unsigned reasoning
	// cannot be verified there.
	other := core.Message{Role: core.MsgAssistant, Parts: []core.Part{
		{Type: core.PartThinking, Text: "x", Sig: "sig", SigKind: core.KindAnthropic},
		{Type: core.PartThinking, Text: "unsigned", SigKind: core.KindBedrock},
		{Type: core.PartText, Text: "answer"},
	}}
	got, _ = json.Marshal(buildMessages([]core.Message{userText("q"), other}, false, true)[1])
	if string(got) != `{"role":"assistant","content":[{"text":"answer"}]}` {
		t.Fatalf("foreign reasoning kept: %s", got)
	}

	// A fallback from Claude to Nova in the same provider: Nova refuses
	// reasoningContent ("This model doesn't support the
	// reasoningContent.reasoningText.signature field"), so it is left out,
	// while an application inference profile (opaque model) keeps it.
	history := []core.Message{userText("Should I take an umbrella in Oslo today?"), resp.Message,
		{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{
			CallID: "tooluse_k3cMZ8sgoQYYQEnRl1CEdk", Name: "get_weather", Content: "rain"}}}}}
	for model, keep := range map[string]bool{
		"us.amazon.nova-pro-v1:0":          false,
		"global.amazon.nova-2-lite-v1:0":   false,
		"meta.llama3-3-70b-instruct-v1:0":  false,
		"global.anthropic.claude-opus-5-5": true,
		"arn:aws:bedrock:us-west-2:111122223333:application-inference-profile/a1b2c3d4e5f6": true,
	} {
		r := buildRequest(&core.Request{Model: model, Tools: []core.ToolDef{{Name: "get_weather"}}, Messages: history})
		s := toJSON(t, r.Messages)
		if strings.Contains(s, "reasoningContent") != keep {
			t.Errorf("%s: reasoning kept %v: %s", model, !keep, s)
		}
		if !strings.Contains(s, `"toolUse"`) || !strings.Contains(s, `"toolResult"`) {
			t.Errorf("%s: tool blocks lost: %s", model, s)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestStreamRedactedReasoning(t *testing.T) {
	resp, _, _, err := streamFrames(t, readFrames(t, "composed_redacted_reasoning.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Message.Parts
	if len(p) != 3 || p[0].Sig != RedactedPrefix+"RXhhbXBsZSByZWRhY3RlZCByZWFzb25pbmc=" || p[0].Text != "" ||
		p[1].Text != "Plain reasoning." || p[1].Sig != "c2lnbmF0dXJlLWV4YW1wbGU=" || p[2].Text != "Done." {
		t.Fatalf("parts %+v", p)
	}
	for _, x := range p[:2] {
		if x.Type != core.PartThinking || x.SigKind != core.KindBedrock {
			t.Fatalf("part %+v", x)
		}
	}
	got, _ := json.Marshal(buildMessages([]core.Message{userText("q"), resp.Message}, false, true)[1].Content[:2])
	want := `[{"reasoningContent":{"redactedContent":"RXhhbXBsZSByZWRhY3RlZCByZWFzb25pbmc="}},{"reasoningContent":{"reasoningText":{"text":"Plain reasoning.","signature":"c2lnbmF0dXJlLWV4YW1wbGU="}}}]`
	if string(got) != want {
		t.Fatalf("round trip %s", got)
	}
}

func TestStreamUsageWithCache(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    core.Usage
	}{
		{"live_cache_write.jsonl", core.Usage{Input: 9, Output: 5, CacheWrite: 1982}},
		{"live_cache_read.jsonl", core.Usage{Input: 9, Output: 5, CacheRead: 1982}},
		{"live_nova_tool.jsonl", core.Usage{Input: 217, Output: 18, CacheWrite: 186}},
	} {
		resp, _, _, err := streamFrames(t, readFrames(t, tc.fixture), nil)
		if err != nil {
			t.Fatal(err)
		}
		// inputTokens excludes cache reads and writes, as core.Usage wants.
		if resp.Usage != tc.want {
			t.Errorf("%s: usage %+v, want %+v", tc.fixture, resp.Usage, tc.want)
		}
	}
}

func TestStreamStopReasons(t *testing.T) {
	resp, _, _, err := streamFrames(t, readFrames(t, "live_max_tokens.jsonl"), nil)
	if err != nil || resp.Stop != core.StopMaxTokens || resp.Message.PlainText() != "One, two" {
		t.Fatalf("max_tokens: %v %+v", err, resp)
	}
	resp, _, _, err = streamFrames(t, readFrames(t, "live_image.jsonl"), nil)
	if err != nil || resp.Stop != core.StopEnd || resp.Message.PlainText() != "red" {
		t.Fatalf("image answer: %v %+v", err, resp)
	}
	for in, want := range map[string]core.StopReason{
		"end_turn": core.StopEnd, "stop_sequence": core.StopEnd, "tool_use": core.StopTool,
		"max_tokens": core.StopMaxTokens, "model_context_window_exceeded": core.StopMaxTokens,
		"guardrail_intervened": core.StopRefusal, "content_filtered": core.StopRefusal,
		"malformed_tool_use": core.StopEnd, "malformed_model_output": core.StopEnd,
	} {
		if got := mapStop(in); got != want {
			t.Errorf("mapStop(%q) = %q, want %q", in, got, want)
		}
	}
	// A tool call cut off by max_tokens never reaches the message.
	frames := []frame{
		event("messageStart", `{"role":"assistant"}`),
		event("contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"tooluse_1","name":"fs_write"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"path\": \"/workspace/rep"}}}`),
		event("contentBlockStop", `{"contentBlockIndex":0}`),
		event("messageStop", `{"stopReason":"max_tokens"}`),
		event("metadata", `{"usage":{"inputTokens":5,"outputTokens":7,"totalTokens":12}}`),
	}
	resp, _, _, err = streamFrames(t, frames, nil)
	if err != nil || resp.Stop != core.StopMaxTokens {
		t.Fatalf("%v %+v", err, resp)
	}
	for _, p := range resp.Message.Parts {
		if p.Type == core.PartToolCall {
			t.Fatalf("truncated call kept: %+v", p.ToolCall)
		}
	}
}

func TestStreamErrors(t *testing.T) {
	_, ev, _, err := streamFrames(t, readFrames(t, "composed_midstream_exception.jsonl"), nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindRateLimit || pe.Status != http.StatusOK || !pe.Retryable() ||
		pe.Msg != "throttlingException: Too many requests, please wait before trying again." {
		t.Fatalf("mid-stream exception: %#v", err)
	}
	if ev.Text() != "Hello from Bedrock" {
		t.Fatalf("events before the exception %q", ev.Text())
	}
	_, _, _, err = streamFrames(t, readFrames(t, "composed_model_stream_error.jsonl"), nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer || !strings.Contains(pe.Msg, "modelStreamErrorException") {
		t.Fatalf("model stream error: %v", err)
	}
	_, _, _, err = streamFrames(t, readFrames(t, "composed_truncated.jsonl"), nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindNetwork || !strings.Contains(pe.Msg, "messageStop") {
		t.Fatalf("truncated stream: %v", err)
	}
	errFrame := frame{Headers: map[string]string{":message-type": "error", ":error-code": "InternalFailure", ":error-message": "boom"}}
	_, _, _, err = streamFrames(t, []frame{event("messageStart", `{"role":"assistant"}`), errFrame}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer || pe.Msg != "InternalFailure: boom" {
		t.Fatalf("error frame: %v", err)
	}
	for name, ex := range map[string]core.ErrorKind{"validationException": core.ErrKindInvalidRequest,
		"serviceUnavailableException": core.ErrKindOverloaded, "internalServerException": core.ErrKindServer} {
		_, _, _, err = streamFrames(t, []frame{exception(name, `{"message":"m"}`)}, nil)
		if !errors.As(err, &pe) || pe.Kind != ex {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A corrupted frame is a server fault the facade may retry.
	b := encodeFrames(t, readFrames(t, "live_text.jsonl"))
	b[len(b)-1] ^= 0xff
	srv := wiretest.NewServer(t, eventStream(b))
	c, _ := testClient(srv)
	_, err = c.Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer || !strings.Contains(pe.Msg, "malformed") {
		t.Fatalf("bad CRC: %v", err)
	}
	srv = wiretest.NewServer(t, wiretest.JSON(http.StatusOK, []byte(`{"output":{}}`)))
	c, _ = testClient(srv)
	_, err = c.Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer {
		t.Fatalf("non-event-stream reply: %v", err)
	}
}

// readSnapshot parses an aws-sdk-go-v2 response snapshot: status line,
// headers, blank line, body.
func readSnapshot(t *testing.T, name string) wiretest.Response {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(b))
	line, _ := r.ReadString('\n')
	status, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for {
		l, _ := r.ReadString('\n')
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		k, v, _ := strings.Cut(l, ":")
		h.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	var body bytes.Buffer
	_, _ = body.ReadFrom(r)
	return wiretest.Response{Status: status, Header: h, Body: body.Bytes()}
}

func TestHTTPErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		kind    core.ErrorKind
		status  int
	}{
		{"sdk_ThrottlingException.error.snap", core.ErrKindRateLimit, 429},
		{"sdk_ServiceQuotaExceededException.error.snap", core.ErrKindRateLimit, 400},
		{"sdk_ModelNotReadyException.error.snap", core.ErrKindOverloaded, 429},
		{"sdk_ServiceUnavailableException.error.snap", core.ErrKindOverloaded, 503},
		{"sdk_AccessDeniedException.error.snap", core.ErrKindAuth, 403},
		{"sdk_ValidationException.error.snap", core.ErrKindInvalidRequest, 400},
		{"sdk_ResourceNotFoundException.error.snap", core.ErrKindInvalidRequest, 404},
		{"sdk_ConflictException.error.snap", core.ErrKindInvalidRequest, 400},
		{"sdk_ModelTimeoutException.error.snap", core.ErrKindServer, 408},
		{"sdk_ModelErrorException.error.snap", core.ErrKindServer, 424},
		{"sdk_InternalServerException.error.snap", core.ErrKindServer, 500},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			srv := wiretest.NewServer(t, readSnapshot(t, tc.fixture))
			c, _ := testClient(srv)
			_, err := c.Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil)
			var pe *core.ProviderError
			if !errors.As(err, &pe) || pe.Kind != tc.kind || pe.Status != tc.status {
				t.Fatalf("err %#v, want kind %s status %d", err, tc.kind, tc.status)
			}
			if !strings.HasSuffix(pe.Msg, ": __Message__") {
				t.Fatalf("msg %q", pe.Msg)
			}
		})
	}
}

func TestHTTPErrorFromHeaderAndCredentials(t *testing.T) {
	var doc struct {
		Status int             `json:"status"`
		Type   string          `json:"x_amzn_errortype"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(wiretest.File(t, "testdata/live_error_unknown_model.json"), &doc); err != nil {
		t.Fatal(err)
	}
	srv := wiretest.NewServer(t, wiretest.Response{Status: doc.Status, ContentType: "application/json",
		Header: http.Header{"X-Amzn-Errortype": {doc.Type}}, Body: doc.Body})
	c, creds := testClient(srv)
	_, err := c.Stream(context.Background(), &core.Request{Model: "us.anthropic.claude-does-not-exist-v1:0", Messages: []core.Message{userText("hi")}}, nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest || pe.Msg != "ValidationException: The provided model identifier is invalid." {
		t.Fatalf("live error: %#v", err)
	}
	if creds.invalidated != 0 {
		t.Fatal("an invalid request must not drop credentials")
	}

	for _, tc := range []struct {
		status int
		header string
		body   string
		kind   core.ErrorKind
		retry  time.Duration
	}{
		{403, "ExpiredTokenException:http://internal.amazon.com/coral/com.amazon.coral.service/", `{"message":"The security token included in the request is expired"}`, core.ErrKindAuth, 0},
		{403, "", `{"__type":"com.amazon.coral.service#UnrecognizedClientException","message":"The security token included in the request is invalid."}`, core.ErrKindAuth, 0},
		{400, "ValidationException", `{"message":"Input is too long for requested model."}`, core.ErrKindContextLength, 0},
		{400, "ValidationException", `{"message":"prompt is too long: 250000 tokens > 200000 maximum"}`, core.ErrKindContextLength, 0},
		{429, "ThrottlingException", `{"message":"Too many tokens, please wait before trying again."}`, core.ErrKindRateLimit, 3 * time.Second},
		{502, "", `<html>bad gateway</html>`, core.ErrKindServer, 0},
	} {
		h := http.Header{}
		if tc.header != "" {
			h.Set("X-Amzn-Errortype", tc.header)
		}
		if tc.retry > 0 {
			h.Set("Retry-After", "3")
		}
		srv := wiretest.NewServer(t, wiretest.Response{Status: tc.status, ContentType: "application/json", Header: h, Body: []byte(tc.body)})
		c, creds := testClient(srv)
		_, err := c.Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil)
		if !errors.As(err, &pe) || pe.Kind != tc.kind || pe.RetryAfter != tc.retry {
			t.Errorf("%d %s %s: %#v", tc.status, tc.header, tc.body, err)
			continue
		}
		if (tc.kind == core.ErrKindAuth) != (creds.invalidated == 1) {
			t.Errorf("%s: invalidated %d times", tc.body, creds.invalidated)
		}
	}

	// Access denied messages name the caller's role ARN: the account id
	// must not reach health state or logs.
	srv = wiretest.NewServer(t, wiretest.Response{Status: 403, ContentType: "application/json",
		Header: http.Header{"X-Amzn-Errortype": {"AccessDeniedException"}},
		Body:   []byte(`{"message":"User: arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev/me is not authorized to perform: bedrock:InvokeModelWithResponseStream"}`)})
	c, _ = testClient(srv)
	_, err = c.Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil)
	if !errors.As(err, &pe) || strings.Contains(err.Error(), "123456789012") || !strings.Contains(pe.Msg, "arn:aws:sts::<account>:assumed-role") {
		t.Fatalf("account id not scrubbed: %v", err)
	}

	// Credentials that cannot be resolved are an auth failure; a stopped
	// run returns its cause instead.
	srv = wiretest.NewServer(t, eventStream(nil))
	c, creds = testClient(srv)
	creds.err = errors.New("the SSO session associated with this profile has expired or is otherwise invalid")
	_, err = c.Stream(context.Background(), &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindAuth || !strings.Contains(pe.Msg, "AWS credentials unavailable: the SSO session") {
		t.Fatalf("credential failure: %v", err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(core.ErrStopped)
	if _, err = c.Stream(ctx, &core.Request{Model: "m", Messages: []core.Message{userText("hi")}}, nil); !errors.Is(err, core.ErrStopped) {
		t.Fatalf("stopped: %v", err)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("%d requests sent without credentials", n)
	}
}

// verifySigV4 recomputes a request's SigV4 signature from the signing key
// derivation of the AWS documentation, independent of the SDK signer.
func verifySigV4(t *testing.T, r wiretest.Recorded, host, secret, region string) {
	t.Helper()
	auth := r.Header.Get("Authorization")
	const prefix = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(auth, prefix) {
		t.Fatalf("authorization %q", auth)
	}
	fields := map[string]string{}
	for _, kv := range strings.Split(strings.TrimPrefix(auth, prefix), ", ") {
		k, v, _ := strings.Cut(kv, "=")
		fields[k] = v
	}
	date := r.Header.Get("X-Amz-Date")
	scope := date[:8] + "/" + region + "/bedrock/aws4_request"
	if fields["Credential"] != testAccessKey+"/"+scope {
		t.Fatalf("credential %q", fields["Credential"])
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Non-S3 services sign the URI-encoded form of the path as sent.
	canonURI := strings.ReplaceAll(u.EscapedPath(), "%", "%25")
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cq []string
	for _, k := range keys {
		for _, v := range q[k] {
			cq = append(cq, url.QueryEscape(k)+"="+strings.ReplaceAll(url.QueryEscape(v), "+", "%20"))
		}
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	var ch strings.Builder
	for _, h := range signed {
		v := r.Header.Get(h)
		if h == "host" {
			v = host
		}
		ch.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	sum := sha256.Sum256(r.Body)
	canon := strings.Join([]string{r.Method, canonURI, strings.Join(cq, "&"), ch.String(), fields["SignedHeaders"], hex.EncodeToString(sum[:])}, "\n")
	cs := sha256.Sum256([]byte(canon))
	sts := "AWS4-HMAC-SHA256\n" + date + "\n" + scope + "\n" + hex.EncodeToString(cs[:])
	mac := func(key []byte, s string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(s))
		return h.Sum(nil)
	}
	k := mac([]byte("AWS4"+secret), date[:8])
	k = mac(k, region)
	k = mac(k, "bedrock")
	k = mac(k, "aws4_request")
	if got := hex.EncodeToString(mac(k, sts)); got != fields["Signature"] {
		t.Fatalf("signature mismatch\ncanonical request:\n%s", canon)
	}
}

func TestRequestIsSignedAndEscaped(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse-stream"},
		{"arn:aws:bedrock:us-west-2:111122223333:application-inference-profile/a1b2c3",
			"/model/arn%3Aaws%3Abedrock%3Aus-west-2%3A111122223333%3Aapplication-inference-profile%2Fa1b2c3/converse-stream"},
	} {
		resp, _, srv, err := streamFrames(t, readFrames(t, "live_text.jsonl"),
			&core.Request{Model: tc.model, MaxTokens: 10, Messages: []core.Message{userText("hi")}})
		if err != nil || resp == nil {
			t.Fatal(err)
		}
		r := srv.Requests()[0]
		if r.Method != http.MethodPost || r.URL != tc.path {
			t.Fatalf("request %s %s, want %s", r.Method, r.URL, tc.path)
		}
		if r.Header.Get("X-Amz-Date") != "20261014T090000Z" || r.Header.Get("X-Amz-Security-Token") != testToken ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers %v", r.Header)
		}
		host := strings.TrimPrefix(srv.URL, "http://")
		verifySigV4(t, r, host, testSecret, "us-west-2")
		for k, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(v, testSecret) {
					t.Fatalf("header %s carries the secret key", k)
				}
			}
		}
		if bytes.Contains(r.Body, []byte(testSecret)) || bytes.Contains(r.Body, []byte(testToken)) {
			t.Fatal("body carries credentials")
		}
	}
}

func TestNewDefaults(t *testing.T) {
	c := New(Options{Region: "eu-central-1", Credentials: aws.AnonymousCredentials{}})
	if c.o.BaseURL != "https://bedrock-runtime.eu-central-1.amazonaws.com" || c.o.ControlURL != "https://bedrock.eu-central-1.amazonaws.com" ||
		c.Kind() != core.KindBedrock || c.o.Kind != core.KindBedrock {
		t.Fatalf("defaults %+v", c.o)
	}
	if RuntimeURL("cn-north-1") != "https://bedrock-runtime.cn-north-1.amazonaws.com.cn" {
		t.Fatal(RuntimeURL("cn-north-1"))
	}
	if !ValidRegion("us-gov-west-1") || ValidRegion("us-west-2.evil.com") || ValidRegion("") {
		t.Fatal("ValidRegion")
	}
	if escapeSegment("a:b/c d~_.-") != "a%3Ab%2Fc%20d~_.-" {
		t.Fatal(escapeSegment("a:b/c d~_.-"))
	}
	if ScrubAccounts("arn:aws:iam::123456789012:role/x 1234567890123 12345678901") != "arn:aws:iam::<account>:role/x 1234567890123 12345678901" {
		t.Fatal(ScrubAccounts("arn:aws:iam::123456789012:role/x"))
	}
}
