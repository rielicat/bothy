package mcp

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// The config loader validates mcp.servers too; these checks repeat the
// security-relevant ones so a server that slips past it (or a future layer
// that skips it) still cannot fetch code at run time or install a range.
var (
	serverNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	npmVersionRe  = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	pypiVersionRe = regexp.MustCompile(`^\d+(?:\.\d+)*(?:(?:a|b|rc)\d+)?(?:\.post\d+)?(?:\.dev\d+)?$`)
	// Package names reach installer argv; requiring a leading letter or digit
	// keeps them from being read as flags.
	npmNameRe       = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
	pypiNameRe      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	npmIntegrityRe  = regexp.MustCompile(`^sha(?:256|384|512)-[A-Za-z0-9+/]+={0,2}$`)
	pypiIntegrityRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// serverSpec is one validated, normalised mcp.servers entry.
type serverSpec struct {
	raw           config.MCPServer
	name          string
	transport     string // stdio | http
	enabled       bool
	url           string // http
	hostPort      string // http: exact host:port for the system dialer
	allowPrivate  bool
	headers       map[string]string // secret references, resolved per session
	profile       core.BoxProfile
	pkg           config.MCPPackage
	command       []string
	egress        []string // host:port
	workspace     bool
	sideEffects   map[string]string
	private       []string
	core          []string
	trustedOutput bool
}

func newServerSpec(c config.MCPServer) (*serverSpec, error) {
	s := &serverSpec{raw: c, name: c.Name, transport: c.Transport, enabled: c.Enabled == nil || *c.Enabled,
		allowPrivate: c.AllowPrivate, headers: c.Headers, sideEffects: c.SideEffects, private: c.Private, core: c.Core,
		trustedOutput: c.TrustedOutput, workspace: c.Workspace == "principal"}
	if !serverNameRe.MatchString(c.Name) {
		return nil, fmt.Errorf("invalid server name %q", c.Name)
	}
	for glob, class := range c.SideEffects {
		if _, err := core.ParseSideEffect(class); err != nil {
			return nil, fmt.Errorf("side_effects.%s: %w", glob, err)
		}
	}
	switch c.Transport {
	case "http":
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
			return nil, fmt.Errorf("url %q must be an http(s) URL without credentials", c.URL)
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		s.url, s.hostPort = u.String(), net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	case "stdio":
		if err := checkPackage(c.Package); err != nil {
			return nil, err
		}
		s.pkg = *c.Package
		if len(c.Command) == 0 || c.Command[0] == "" {
			return nil, fmt.Errorf("command is required for stdio servers")
		}
		if f := runtimeFetcher(c.Command); f != "" {
			return nil, fmt.Errorf("command %s fetches packages at run time; install the package and run it from /opt/mcp", f)
		}
		s.command = slices.Clone(c.Command)
		s.profile = core.BoxProfile(c.Profile)
		if s.profile == "" {
			s.profile = core.ProfileBase
			if s.pkg.Ecosystem == "npm" {
				s.profile = core.ProfileNode
			}
		}
		for _, e := range c.Egress {
			hp, err := normHostPort(e)
			if err != nil {
				return nil, fmt.Errorf("egress %q: %w", e, err)
			}
			s.egress = append(s.egress, hp)
		}
	default:
		return nil, fmt.Errorf("unknown transport %q", c.Transport)
	}
	return s, nil
}

// runtimeFetcher names the package runner a command starts with ("npx",
// "pipx run", ...), or returns "". SPEC §2.11 names npx, uvx, bunx and pipx
// run; the equivalent sub-commands of npm, pnpm, yarn, bun and uv fetch just
// the same, so they are refused too.
func runtimeFetcher(cmd []string) string {
	exe, sub := filepath.Base(cmd[0]), ""
	if len(cmd) > 1 {
		sub = cmd[1]
	}
	switch {
	case exe == "npx", exe == "uvx", exe == "bunx", exe == "pnpx":
		return exe
	case exe == "pipx" && sub == "run", exe == "npm" && (sub == "exec" || sub == "x"),
		exe == "pnpm" && sub == "dlx", exe == "yarn" && sub == "dlx", exe == "bun" && sub == "x":
		return exe + " " + sub
	case exe == "uv" && sub == "tool" && len(cmd) > 2 && cmd[2] == "run":
		return "uv tool run"
	}
	return ""
}

func checkPackage(p *config.MCPPackage) error {
	if p == nil {
		return fmt.Errorf("stdio servers need package: {ecosystem, name, version, integrity}")
	}
	var nameRe, versionRe, integrityRe *regexp.Regexp
	switch p.Ecosystem {
	case "npm":
		nameRe, versionRe, integrityRe = npmNameRe, npmVersionRe, npmIntegrityRe
	case "pypi":
		nameRe, versionRe, integrityRe = pypiNameRe, pypiVersionRe, pypiIntegrityRe
	default:
		return fmt.Errorf("package.ecosystem %q must be npm or pypi", p.Ecosystem)
	}
	switch {
	case !nameRe.MatchString(p.Name):
		return fmt.Errorf("package.name %q is not a valid %s package name", p.Name, p.Ecosystem)
	case !versionRe.MatchString(p.Version):
		return fmt.Errorf("package.version must be an exact version (no latest, ranges or git refs), got %q", p.Version)
	case !integrityRe.MatchString(p.Integrity):
		return fmt.Errorf("package.integrity %q is not a %s integrity hash", p.Integrity, p.Ecosystem)
	}
	return nil
}

// normHostPort turns an egress entry into the exact host:port the box may
// reach (port 443 by default). Wildcards are refused: box extras are exact.
func normHostPort(s string) (string, error) {
	host, port := strings.ToLower(s), "443"
	if h, p, err := net.SplitHostPort(host); err == nil {
		host, port = h, p
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	n, err := strconv.Atoi(port)
	if host == "" || strings.ContainsAny(host, "/@ *[]") || err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("want an exact host or host:port")
	}
	return net.JoinHostPort(host, port), nil
}

// globMatch reports whether a config tool glob ("read_*") matches tool.
func globMatch(glob, tool string) bool {
	ok, err := path.Match(glob, tool)
	return err == nil && ok
}

// suggestedClass is the side effect recorded with a newly seen tool: the most
// specific matching side_effects glob (an exact name beats any pattern,
// longer patterns beat shorter ones), else the "default" key, else
// external_send. Annotations only ever raise it (annotationFloor).
func (s *serverSpec) suggestedClass(tool string, def *toolDefinition) core.SideEffect {
	best := ""
	better := func(g string) bool {
		switch {
		case best == "" || g == tool:
			return true
		case best == tool:
			return false
		case len(g) != len(best):
			return len(g) > len(best)
		}
		return g < best
	}
	for glob := range s.sideEffects {
		if glob != "default" && globMatch(glob, tool) && better(glob) {
			best = glob
		}
	}
	if best == "" {
		best = "default"
	}
	class := core.SEExternalSend
	if v, ok := s.sideEffects[best]; ok {
		// Values were checked by newServerSpec.
		class, _ = core.ParseSideEffect(v)
	}
	return max(class, def.annotationFloor())
}

// privateOutput reports whether config marks the tool's output as private.
func (s *serverSpec) privateOutput(tool string) bool {
	return slices.ContainsFunc(s.private, func(g string) bool { return globMatch(g, tool) })
}

// pinnedCore reports whether config pins the tool into the core tools block.
func (s *serverSpec) pinnedCore(tool string) bool {
	return slices.ContainsFunc(s.core, func(g string) bool { return globMatch(g, tool) })
}

func (s *serverSpec) installTree(id string) string { return "mcp/" + s.name + "/" + id }

// registryHosts are the only destinations an installer box may reach.
func (s *serverSpec) registryHosts() []string {
	if s.pkg.Ecosystem == "pypi" {
		return []string{"pypi.org:443", "files.pythonhosted.org:443"}
	}
	return []string{"registry.npmjs.org:443"}
}
