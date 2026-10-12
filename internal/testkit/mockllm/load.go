package mockllm

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/bothyhq/bothy/internal/core"
)

// script is the YAML file format: either this mapping or a bare list of rules.
//
//	models:
//	  - {id: mock-16k, context: 16384, max_output: 2048, tools: true}
//	rules:
//	  - match_user: weather
//	    tool_calls: [{name: web_search, args: {query: weather}}]
//	    times: 1
//	  - text: ["It is ", "sunny."]
//	    usage: {input: 120, output: 4, cache_read: 100}
//	    delay: 20ms
type script struct {
	Models []yamlModel `yaml:"models"`
	Rules  []yamlRule  `yaml:"rules"`
}

type yamlModel struct {
	ID        string `yaml:"id"`
	Provider  string `yaml:"provider"`
	Context   int    `yaml:"context"`
	MaxOutput int    `yaml:"max_output"`
	Tools     bool   `yaml:"tools"`
	Vision    bool   `yaml:"vision"`
}

// yamlRule mirrors Rule with YAML-friendly tool calls and usage: core.ToolCall
// args are raw JSON and core.Usage has no yaml tags.
type yamlRule struct {
	MatchUser       string         `yaml:"match_user"`
	MatchToolResult string         `yaml:"match_tool_result"`
	MatchSystem     string         `yaml:"match_system"`
	Text            []string       `yaml:"text"`
	ToolCalls       []yamlToolCall `yaml:"tool_calls"`
	Usage           *yamlUsage     `yaml:"usage"`
	NoUsage         bool           `yaml:"no_usage"`
	Fault           string         `yaml:"fault"`
	Delay           time.Duration  `yaml:"delay"`
	Times           int            `yaml:"times"`
}

type yamlToolCall struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	Args any    `yaml:"args"` // a mapping, or a string holding JSON
}

type yamlUsage struct {
	Input      int `yaml:"input"`
	Output     int `yaml:"output"`
	CacheRead  int `yaml:"cache_read"`
	CacheWrite int `yaml:"cache_write"`
	Reasoning  int `yaml:"reasoning"`
}

// Load starts a server from a YAML script file (format: see script).
func Load(path string) (*Server, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rules, models, err := parseScript(b)
	if err != nil {
		return nil, fmt.Errorf("mockllm: %s: %w", path, err)
	}
	compiled, err := compile(rules)
	if err != nil {
		return nil, fmt.Errorf("mockllm: %s: %w", path, err)
	}
	return start(compiled, models), nil
}

func parseScript(b []byte) ([]Rule, []core.ModelInfo, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	var sc script
	if len(doc.Content) > 0 && doc.Content[0].Kind == yaml.SequenceNode {
		if err := doc.Content[0].Decode(&sc.Rules); err != nil {
			return nil, nil, err
		}
	} else if err := doc.Decode(&sc); err != nil {
		return nil, nil, err
	}
	models := make([]core.ModelInfo, len(sc.Models))
	for i, m := range sc.Models {
		if m.ID == "" {
			return nil, nil, fmt.Errorf("model %d: id is required", i)
		}
		provider := m.Provider
		if provider == "" {
			provider = "mock"
		}
		models[i] = core.ModelInfo{Ref: core.ModelRef{Provider: provider, Model: m.ID}, Display: m.ID,
			Context: m.Context, MaxOutput: m.MaxOutput, Tools: m.Tools, Vision: m.Vision, Source: "config"}
	}
	rules := make([]Rule, len(sc.Rules))
	for i, yr := range sc.Rules {
		r := Rule{MatchUser: yr.MatchUser, MatchToolResult: yr.MatchToolResult, MatchSystem: yr.MatchSystem,
			Text: yr.Text, NoUsage: yr.NoUsage, Fault: yr.Fault, Delay: yr.Delay, Times: yr.Times}
		if u := yr.Usage; u != nil {
			r.Usage = &core.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning}
		}
		for j, tc := range yr.ToolCalls {
			args, err := toJSON(tc.Args)
			if err != nil {
				return nil, nil, fmt.Errorf("rule %d tool call %d: %w", i, j, err)
			}
			r.ToolCalls = append(r.ToolCalls, core.ToolCall{ID: tc.ID, Name: tc.Name, Args: args})
		}
		rules[i] = r
	}
	return rules, models, nil
}

func toJSON(v any) (json.RawMessage, error) {
	switch x := v.(type) {
	case nil:
		return json.RawMessage("{}"), nil
	case string:
		if !json.Valid([]byte(x)) {
			return nil, fmt.Errorf("args string is not JSON: %q", x)
		}
		return json.RawMessage(x), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("args: %w", err)
	}
	return b, nil
}
