package sandbox

import (
	"context"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

// check is a doctor check computed from the current sandbox status.
type check struct {
	id  string
	svc *service
	run func(st core.SandboxStatus) []core.Finding
}

func (c check) ID() string { return c.id }

func (c check) Run(ctx context.Context) []core.Finding { return c.run(c.svc.Status(ctx)) }

func (c check) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

const daemonJSON = `{ "runtimes": { "runsc": { "path": "/usr/local/bin/runsc", "runtimeArgs": ["--host-uds=open"] } } }`

func checks(s *service) []core.Check {
	finding := func(id, title, detail, fix string, sev core.Severity, security bool) []core.Finding {
		return []core.Finding{{ID: id, Area: "sandbox", Title: title, Detail: detail, Fix: fix, Severity: sev, Security: security}}
	}
	return []core.Check{
		check{id: "sandbox.unavailable", svc: s, run: func(st core.SandboxStatus) []core.Finding {
			if st.Available || len(isolationFailure(st)) > 0 {
				return nil
			}
			fix := st.Fix
			if fix == "" {
				fix = composeFix
			}
			return finding("sandbox.unavailable", "No sandbox: shell, python and MCP stdio are off",
				orDefault(st.Reason, noSandboxText), fix, core.SevMedium, true)
		}},
		check{id: "sandbox.not_isolated", svc: s, run: func(st core.SandboxStatus) []core.Finding {
			bad := isolationFailure(st)
			if len(bad) == 0 {
				return nil
			}
			var details []string
			for _, c := range st.SelfTest {
				if !c.OK {
					details = append(details, c.Name+": "+c.Detail)
				}
			}
			return finding("sandbox.not_isolated", "Sandbox self-test failed: exec is refused",
				strings.Join(details, "; "), st.Fix, core.SevCritical, true)
		}},
		check{id: "sandbox.rootful_runc", svc: s, run: func(st core.SandboxStatus) []core.Finding {
			if st.Backend != "sandboxd" || !st.Available || st.Runtime != "runc" || st.Rootless {
				return nil
			}
			return finding("sandbox.rootful_runc", "Tool boxes run under runc on rootful Docker",
				"A container escape from a box would land as an unprivileged uid on a root-owned engine. Prefer rootless Docker or gVisor (runsc).",
				"use rootless Docker, enable userns-remap, or register runsc with: "+daemonJSON, core.SevLow, true)
		}},
		check{id: "sandbox.runsc_host_uds", svc: s, run: func(st core.SandboxStatus) []core.Finding {
			c, ok := selfTestCheck(st, proto.CheckRunsc)
			if !ok || c.OK {
				return nil
			}
			return finding("sandbox.runsc_host_uds", "gVisor (runsc) is registered but its canary failed",
				c.Detail, "set the runsc runtime args in /etc/docker/daemon.json to: "+daemonJSON+" and restart Docker",
				core.SevLow, false)
		}},
		// SPEC §2.12: bwrap gets memory/pids limits only from cgroup v2
		// delegation, which this backend does not set up, so doctor says so.
		check{id: "sandbox.bwrap_limits", svc: s, run: func(st core.SandboxStatus) []core.Finding {
			if st.Backend != "bwrap" || !st.Available {
				return nil
			}
			return finding("sandbox.bwrap_limits", "bwrap jails have no memory or process limits",
				"Jails get rlimits only; a runaway command can exhaust the memory or the processes of the user the gateway runs as.",
				"use the compose deployment for per-box memory, CPU and pids limits: "+composeFix, core.SevLow, true)
		}},
		check{id: "docker.socket_access", svc: s, run: func(st core.SandboxStatus) []core.Finding {
			c, ok := selfTestCheck(st, proto.CheckEngine)
			if !ok || c.OK || !strings.HasPrefix(c.Detail, "cannot access") {
				return nil
			}
			return []core.Finding{{ID: "docker.socket_access", Area: "docker", Title: "sandboxd cannot access the Docker socket",
				Detail: c.Detail, Fix: st.Fix, Severity: core.SevMedium}}
		}},
	}
}

func selfTestCheck(st core.SandboxStatus, name string) (core.SelfTestCheck, bool) {
	for _, c := range st.SelfTest {
		if c.Name == name {
			return c, true
		}
	}
	return core.SelfTestCheck{}, false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
