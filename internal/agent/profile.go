package agent

import (
	"slices"
	"strconv"
	"strings"

	"github.com/bothyhq/bothy/internal/config"
)

// profile is the model profile chosen from the effective window (SPEC §5.6).
type profile string

const (
	profileStandard profile = "standard"
	profileCompact  profile = "compact"
	profileDegraded profile = "degraded"
	profileTextOnly profile = "text-only"
)

// Core tool sets of the small profiles, in the order SPEC §5.6 lists them.
var (
	compactTools  = []string{"shell", "fs_read", "fs_write", "web_search", "web_fetch", "memory", "tool_search", "use_tool"}
	degradedTools = []string{"shell", "fs_read", "fs_write", "web_search", "memory"}
)

// promotedCap bounds how many lazy tools compaction may promote into the
// standard tools block; the small profiles have hard caps instead.
const promotedCap = 6

func profileFor(c *config.Config, window int) profile {
	cc := c.Context
	compactBelow := orInt(cc.CompactBelow, 32768)
	degradedBelow := orInt(cc.DegradedBelow, 16384)
	minContext := orInt(cc.MinContext, 8192)
	switch {
	case window >= compactBelow:
		return profileStandard
	case window >= degradedBelow:
		return profileCompact
	case window >= minContext:
		return profileDegraded
	}
	return profileTextOnly
}

// reserveFor is the output reservation: context.reserve_output as an integer,
// or "auto" per profile. It never exceeds half the window.
func reserveFor(c *config.Config, p profile, window int) int {
	var r int
	if n, err := strconv.Atoi(strings.TrimSpace(c.Context.ReserveOutput)); err == nil && n > 0 {
		r = n
	} else {
		switch p {
		case profileStandard:
			r = min(8192, window*12/100)
		case profileCompact:
			r = 2048
		}
		r = max(r, 1024)
	}
	if window > 0 {
		r = min(r, window/2)
	}
	return r
}

// triggerFor is the share of the window at which compaction starts.
func triggerFor(c *config.Config, p profile) float64 {
	cc := c.Context.Compaction
	switch p {
	case profileStandard:
		return orFloat(cc.Trigger, 0.70)
	case profileCompact:
		return orFloat(cc.CompactTrigger, 0.60)
	}
	return 0.55
}

// toolOutputCap is the per-result cap in estimated tokens.
func toolOutputCap(c *config.Config, p profile, window int) int {
	switch p {
	case profileStandard:
		n := orInt(c.Agent.ToolOutputTokens, 4000)
		if window > 0 {
			n = min(n, window/10)
		}
		return max(n, 500)
	case profileCompact:
		return 1000
	}
	return 500
}

// toolCap is the most core tools a profile's tools block may hold (0 = none,
// -1 = no fixed cap).
func toolCap(p profile) int {
	switch p {
	case profileCompact:
		return len(compactTools)
	case profileDegraded:
		return len(degradedTools)
	case profileTextOnly:
		return 0
	}
	return -1
}

// allowedIn reports whether a tool may appear in a small profile's block.
func allowedIn(p profile, name string) bool {
	switch p {
	case profileCompact:
		return slices.Contains(compactTools, name)
	case profileDegraded:
		return slices.Contains(degradedTools, name)
	case profileTextOnly:
		return false
	}
	return true
}

// alwaysLazy reports the builtins SPEC §5.6 keeps out of every tools block:
// they delete, open egress, read skill files or schedule work, so the model
// reaches them only through tool_search/use_tool.
func alwaysLazy(name string) bool {
	switch name {
	case "fs_delete", "net_allow", "skill_read":
		return true
	}
	return strings.HasPrefix(name, "schedule_")
}

func orInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func orFloat(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}
