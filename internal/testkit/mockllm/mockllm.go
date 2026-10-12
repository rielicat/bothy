// Package mockllm is a scriptable OpenAI-compatible server (/v1/models,
// /v1/chat/completions with and without stream) for agent tests and E2E.
package mockllm

import (
	"time"

	"github.com/bothyhq/bothy/internal/core"
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

type Server struct{}

func New(rules []Rule, models []core.ModelInfo) *Server { return &Server{} }
func Load(path string) (*Server, error)                 { return &Server{}, nil } // YAML script
func (s *Server) URL() string                           { return "" }             // base URL ending in /v1
func (s *Server) Requests() [][]byte                    { return nil }            // raw request bodies, in order
func (s *Server) Close()                                {}
