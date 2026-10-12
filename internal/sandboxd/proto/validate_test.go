package proto

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

const (
	goodKey = "box_0123456789abcdefghjkmnpqrs"
	goodWS  = "ws_0123456789abcdefghjkmnpqrs"
	goodMCP = "mcp/docs-server/in_0123456789abcdefghjkmnpqrs"
)

func TestValidIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(string) bool
		in   string
		want bool
	}{
		{"key", ValidKey, goodKey, true},
		{"key upper", ValidKey, strings.ToUpper(goodKey), false},
		{"key short", ValidKey, "box_123", false},
		{"key traversal", ValidKey, "box_../../../etc/passwd0000", false},
		{"key empty", ValidKey, "", false},
		{"ws", ValidWorkspace, goodWS, true},
		{"ws empty", ValidWorkspace, "", true},
		{"ws slash", ValidWorkspace, "ws_0123456789abcdefghjkmnpq/s", false},
		{"ws dotdot", ValidWorkspace, "..", false},
		{"mcp", ValidMCPTree, goodMCP, true},
		{"mcp empty", ValidMCPTree, "", true},
		{"mcp dotdot", ValidMCPTree, "mcp/../in_0123456789abcdefghjkmnpqrs", false},
		{"mcp absolute", ValidMCPTree, "/mcp/x/in_0123456789abcdefghjkmnpqrs", false},
		{"mcp leading dash", ValidMCPTree, "mcp/-x/in_0123456789abcdefghjkmnpqrs", false},
		{"profile", ValidProfile, "base", true},
		{"profile bad", ValidProfile, "Base!", false},
	} {
		if got := tc.fn(tc.in); got != tc.want {
			t.Errorf("%s: %q = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestEgressSubpath(t *testing.T) {
	got := EgressSubpath(goodKey)
	if !strings.HasPrefix(got, "egress/") || len(got) != len("egress/")+32 {
		t.Fatalf("EgressSubpath = %q, want egress/<32 hex>", got)
	}
	if EgressSubpath(goodKey) != got || EgressSubpath("box_1") == got {
		t.Fatal("EgressSubpath must be a deterministic function of the key")
	}
}

func TestValidateCreate(t *testing.T) {
	profiles := map[string]string{"base": "x"}
	ok := CreateReq{Key: goodKey, Profile: "base", Workspace: goodWS, MCPTree: goodMCP}
	if err := ValidateCreate(&ok, profiles); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for name, r := range map[string]CreateReq{
		"bad key":         {Key: "box_x", Profile: "base"},
		"bad workspace":   {Key: goodKey, Profile: "base", Workspace: "ws_../x"},
		"bad tree":        {Key: goodKey, Profile: "base", MCPTree: "mcp/../../etc"},
		"rw without tree": {Key: goodKey, Profile: "base", MCPTreeRW: true},
		"unknown profile": {Key: goodKey, Profile: "gpu"},
	} {
		if err := ValidateCreate(&r, profiles); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestLimitsClamp(t *testing.T) {
	for _, tc := range []struct{ in, want Limits }{
		{Limits{}, Limits{64, 0.1, 16}},
		{Limits{MemoryMB: 1, CPUs: 0.01, Pids: 1}, Limits{64, 0.1, 16}},
		{Limits{MemoryMB: 1 << 20, CPUs: 64, Pids: 1 << 20}, Limits{2048, 2, 512}},
		{Limits{MemoryMB: 512, CPUs: 1, Pids: 256}, Limits{512, 1, 256}},
		{Limits{MemoryMB: -5, CPUs: math.NaN(), Pids: -1}, Limits{64, 0.1, 16}},
	} {
		if got := tc.in.Clamp(); got != tc.want {
			t.Errorf("Clamp(%+v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestCleanCwd(t *testing.T) {
	for in, want := range map[string]string{"": "", ".": "", "src": "src", "src/app": "src/app", "src/": "src", "a.b/c-d": "a.b/c-d"} {
		got, err := CleanCwd(in)
		if err != nil || got != want {
			t.Errorf("CleanCwd(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"..", "../x", "a/../b", "a/..", "/etc", "a//b", "./a", "a/./b", "a\x00b", `a\b`} {
		if _, err := CleanCwd(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("CleanCwd(%q) err = %v, want ErrInvalid", in, err)
		}
	}
}

func TestValidateEnv(t *testing.T) {
	if err := ValidateEnv(map[string]string{"FOO": "1", "_X9": "", "NODE_ENV": "production"}); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
	for _, k := range []string{"PATH", "HOME", "LD_PRELOAD", "LD_LIBRARY_PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"ALL_PROXY", "BASH_ENV", "NODE_OPTIONS", "PYTHONPATH", "lower", "1ABC", "A-B", "", strings.Repeat("A", 65)} {
		if err := ValidateEnv(map[string]string{k: "x"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("env key %q: err = %v, want ErrInvalid", k, err)
		}
	}
	if err := ValidateEnv(map[string]string{"A": "x\x00y"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("NUL in value: err = %v", err)
	}
	big := map[string]string{}
	for i := range MaxEnv + 1 {
		big[fmt.Sprintf("K%d", i)] = "v"
	}
	if err := ValidateEnv(big); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many keys: err = %v", err)
	}
}

func TestValidateArgv(t *testing.T) {
	if err := ValidateArgv([]string{"bash", "-lc", "echo hi"}); err != nil {
		t.Fatal(err)
	}
	long := make([]string, MaxArgv+1)
	for i := range long {
		long[i] = "x"
	}
	for name, argv := range map[string][]string{
		"empty":     nil,
		"empty0":    {""},
		"nul":       {"echo", "a\x00b"},
		"too many":  long,
		"too large": {"echo", strings.Repeat("x", MaxArgvBytes)},
	} {
		if err := ValidateArgv(argv); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
