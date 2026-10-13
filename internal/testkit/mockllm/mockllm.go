// Package mockllm is a scriptable OpenAI-compatible server (/v1/models,
// /v1/chat/completions with and without stream) for agent tests and E2E.
//
// Each chat completion request is matched against the rules in order; the
// first rule whose matchers all match (and whose Times is not used up) answers
// it. A rule without matchers matches everything. MatchUser applies to the
// text of the last user message; MatchToolResult to the last tool message
// after it, so it never matches a request that does not end a tool round;
// MatchSystem to the system and developer messages joined by newlines.
// Unmatched requests get a 400 invalid_request_error with code "no_rule".
//
// Responses follow the OpenAI wire format: streamed chat.completion.chunk
// frames (role, one frame per Text chunk, tool calls with arguments split
// across frames, a finish frame, a usage frame when
// stream_options.include_usage is set, then [DONE]) or one chat.completion
// object. A request for a model the server does not list is a 404
// model_not_found; a prompt whose estimate (bytes/4) exceeds the model's
// Context gets the overflow error, as from a real server.
//
// Faults: "429" (with Retry-After: 1), "500", "overflow" (400
// context_length_exceeded), "midstream" (the connection drops after half the
// frames or body bytes), "slow_ttft" (nothing, not even headers, is sent for
// Delay, default 30s; the response then streams without further delays) and
// "malformed" (a truncated JSON frame or body).
package mockllm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

type Rule struct {
	MatchUser       string          `yaml:"match_user"`        // regexp on the last user text
	MatchToolResult string          `yaml:"match_tool_result"` // regexp on the last tool result
	MatchSystem     string          `yaml:"match_system"`      // regexp on the system prompt (e.g. compaction requests)
	Text            []string        `yaml:"text"`              // streamed chunks
	ToolCalls       []core.ToolCall `yaml:"tool_calls"`
	Usage           *core.Usage     `yaml:"usage"`    // default: estimated from bytes
	NoUsage         bool            `yaml:"no_usage"` // omit usage (compat quirk)
	Fault           string          `yaml:"fault"`    // "" | 429 | 500 | overflow | midstream | slow_ttft | malformed
	Delay           time.Duration   `yaml:"delay"`    // between chunks
	Times           int             `yaml:"times"`    // 0 = unlimited
}

// Fault values.
const (
	FaultRateLimit = "429"
	FaultServer    = "500"
	FaultOverflow  = "overflow"
	FaultMidstream = "midstream"
	FaultSlowTTFT  = "slow_ttft"
	FaultMalformed = "malformed"
)

var faults = []string{"", FaultRateLimit, FaultServer, FaultOverflow, FaultMidstream, FaultSlowTTFT, FaultMalformed}

// DefaultSlowTTFT is the slow_ttft wait of a rule without Delay.
const DefaultSlowTTFT = 30 * time.Second

// Recorded is one request the server received.
type Recorded struct {
	Method string
	Path   string // including the query string
	Header http.Header
	Body   []byte
}

type Server struct {
	ts     *httptest.Server
	models []core.ModelInfo
	closed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	rules    []compiledRule
	requests [][]byte
	recorded []Recorded
	callSeq  int
	respSeq  int
}

type compiledRule struct {
	Rule
	user, tool, system *regexp.Regexp
	used               int
}

// New starts a server answering with rules; models are listed by /v1/models
// (by Ref.Model) and, when given, restrict the accepted model ids. It panics
// on an invalid rule; Load reports one as an error.
func New(rules []Rule, models []core.ModelInfo) *Server {
	compiled, err := compile(rules)
	if err != nil {
		panic(err)
	}
	return start(compiled, slices.Clone(models))
}

func start(rules []compiledRule, models []core.ModelInfo) *Server {
	s := &Server{rules: rules, models: models, closed: make(chan struct{})}
	s.ts = httptest.NewServer(s.handler())
	return s
}

func compile(rules []Rule) ([]compiledRule, error) {
	out := make([]compiledRule, len(rules))
	for i, r := range rules {
		if !slices.Contains(faults, r.Fault) {
			return nil, fmt.Errorf("mockllm: rule %d: unknown fault %q", i, r.Fault)
		}
		c := compiledRule{Rule: r}
		for _, m := range []struct {
			expr string
			dst  **regexp.Regexp
		}{{r.MatchUser, &c.user}, {r.MatchToolResult, &c.tool}, {r.MatchSystem, &c.system}} {
			if m.expr == "" {
				continue
			}
			re, err := regexp.Compile(m.expr)
			if err != nil {
				return nil, fmt.Errorf("mockllm: rule %d: %w", i, err)
			}
			*m.dst = re
		}
		out[i] = c
	}
	return out, nil
}

func (s *Server) URL() string { return s.ts.URL + "/v1" } // base URL ending in /v1

// Requests returns the raw bodies of the chat completion requests, in order.
func (s *Server) Requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.requests))
	for i, b := range s.requests {
		out[i] = slices.Clone(b)
	}
	return out
}

// History returns every request the server received, in order.
func (s *Server) History() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Recorded, len(s.recorded))
	for i, r := range s.recorded {
		r.Header = r.Header.Clone()
		r.Body = slices.Clone(r.Body)
		out[i] = r
	}
	return out
}

// Close ends in-flight responses (slow ones included) and stops the server.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.closed)
		s.ts.CloseClientConnections()
		s.ts.Close()
	})
}

// turn is what the matchers look at in a request.
type turn struct {
	system, user, toolResult string
	hasToolResult            bool
}

func analyze(msgs []chatMessage) turn {
	var t turn
	var system []string
	lastUser := -1
	for i, m := range msgs {
		switch m.Role {
		case "system", "developer":
			system = append(system, contentText(m.Content))
		case "user":
			lastUser = i
		}
	}
	t.system = strings.Join(system, "\n")
	if lastUser >= 0 {
		t.user = contentText(msgs[lastUser].Content)
	}
	for _, m := range msgs[lastUser+1:] {
		if m.Role == "tool" {
			t.toolResult, t.hasToolResult = contentText(m.Content), true
		}
	}
	return t
}

func (c *compiledRule) matches(t turn) bool {
	if c.Times > 0 && c.used >= c.Times {
		return false
	}
	if c.user != nil && !c.user.MatchString(t.user) {
		return false
	}
	if c.tool != nil && (!t.hasToolResult || !c.tool.MatchString(t.toolResult)) {
		return false
	}
	if c.system != nil && !c.system.MatchString(t.system) {
		return false
	}
	return true
}

// match consumes one use of the first matching rule.
func (s *Server) match(t turn) (Rule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rules {
		if s.rules[i].matches(t) {
			s.rules[i].used++
			return s.rules[i].Rule, true
		}
	}
	return Rule{}, false
}

func (s *Server) model(id string) (core.ModelInfo, bool) {
	if len(s.models) == 0 {
		return core.ModelInfo{Ref: core.ModelRef{Provider: "mock", Model: id}}, true
	}
	for _, m := range s.models {
		if m.Ref.Model == id {
			return m, true
		}
	}
	return core.ModelInfo{}, false
}
