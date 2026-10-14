package bedrock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

// frame is one event-stream message as the fixtures store it: the string
// headers and the payload verbatim.
type frame struct {
	Headers map[string]string `json:"headers"`
	Payload string            `json:"payload"`
}

// encodeFrames turns frames into event-stream bytes (prelude, headers,
// payload and both CRCs), as Bedrock sends them.
func encodeFrames(t testing.TB, frames []frame) []byte {
	t.Helper()
	enc := eventstream.NewEncoder()
	var buf bytes.Buffer
	for _, f := range frames {
		var hs eventstream.Headers
		for _, k := range []string{":event-type", ":exception-type", ":error-code", ":error-message", ":content-type", ":message-type"} {
			if v, ok := f.Headers[k]; ok {
				hs.Set(k, eventstream.StringValue(v))
			}
		}
		if err := enc.Encode(&buf, eventstream.Message{Headers: hs, Payload: []byte(f.Payload)}); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// readFrames loads a JSONL fixture.
func readFrames(t testing.TB, name string) []frame {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []frame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var fr frame
		if err := json.Unmarshal(sc.Bytes(), &fr); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, fr)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// event builds an event frame with a JSON payload.
func event(typ, payload string) frame {
	return frame{Headers: map[string]string{":event-type": typ, ":content-type": "application/json", ":message-type": "event"}, Payload: payload}
}

func exception(typ, payload string) frame {
	return frame{Headers: map[string]string{":exception-type": typ, ":content-type": "application/json", ":message-type": "exception"}, Payload: payload}
}

func eventStream(body []byte) wiretest.Response {
	return wiretest.Response{Status: http.StatusOK, ContentType: eventStreamType, Body: body}
}

// staticCreds is a credential provider with fixed test values that counts
// invalidations.
type staticCreds struct {
	creds       aws.Credentials
	err         error
	invalidated int
}

func (s *staticCreds) Retrieve(context.Context) (aws.Credentials, error) { return s.creds, s.err }
func (s *staticCreds) Invalidate()                                       { s.invalidated++ }

const (
	testAccessKey = "AKIDCANARY7F3A9C0000"
	testSecret    = "secret-CANARY-91f7e2b4c3"
	testToken     = "session-token-CANARY-5d2e"
)

var signTime = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

// testClient returns a client pointed at srv for both endpoints.
func testClient(srv *wiretest.Server) (*Client, *staticCreds) {
	creds := &staticCreds{creds: aws.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecret, SessionToken: testToken, Source: "test"}}
	o := wiretest.Options(srv, "bedrock")
	o.Key = nil
	c := New(Options{Options: o, Region: "us-west-2", ControlURL: srv.URL, Credentials: creds, Now: func() time.Time { return signTime }})
	return c, creds
}

func userText(s string) core.Message {
	return core.Message{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: s}}}
}

// streamFrames runs one Stream call against frames.
func streamFrames(t *testing.T, frames []frame, req *core.Request) (*core.Response, *wiretest.Events, *wiretest.Server, error) {
	t.Helper()
	srv := wiretest.NewServer(t, eventStream(encodeFrames(t, frames)))
	c, _ := testClient(srv)
	if req == nil {
		req = &core.Request{Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", MaxTokens: 64, Messages: []core.Message{userText("Hello")}}
	}
	var ev wiretest.Events
	resp, err := c.Stream(context.Background(), req, ev.On)
	return resp, &ev, srv, err
}
