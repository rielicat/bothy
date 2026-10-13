//go:build runsc

package sandbox

// The runsc job (SPEC §12.2, M5): run on a Docker host with gVisor
// registered. BOTHY_TEST_RUNSC_EXPECT says what the host is set up for:
//
//	runsc     daemon.json passes --host-uds=open: boxes run under runsc and egress works
//	fallback  runsc lacks --host-uds=open, BOTHY_SBX_RUNTIME=auto: runc with a runsc finding
//	refused   runsc lacks --host-uds=open, BOTHY_SBX_RUNTIME=runsc: no boxes at all
//
// MODE=runsc EXPECT=<case> deploy/sandbox/test/acceptance.sh runs it.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

func TestRunsc(t *testing.T) {
	d, st := newDockerEnv(t)
	ctx := context.Background()
	switch expect := os.Getenv("BOTHY_TEST_RUNSC_EXPECT"); expect {
	case "runsc":
		if !st.Available || st.Runtime != "runsc" {
			t.Fatalf("want runsc, got available=%v runtime=%q reason=%q", st.Available, st.Runtime, st.Reason)
		}
		key, _ := d.ensure(t, "runsc")
		curl := "curl -sS -o /dev/null -w '%{http_code}' --max-time 30 https://example.com/"
		if res := d.sh(t, key, curl, time.Minute); res.Exit == 0 {
			t.Fatalf("reached example.com without a grant: %s", res.Stdout)
		}
		d.eg.Allow("example.com:443")
		if res := d.sh(t, key, curl, time.Minute); res.Exit != 0 || string(res.Stdout) != "200" {
			t.Fatalf("egress under runsc: exit %d stdout %q stderr %q", res.Exit, res.Stdout, res.Stderr)
		}
	case "fallback":
		if !st.Available || st.Runtime != "runc" {
			t.Fatalf("want a runc fallback, got available=%v runtime=%q", st.Available, st.Runtime)
		}
		c, ok := selfTestCheck(st, proto.CheckRunsc)
		if !ok || c.OK || !strings.Contains(c.Detail, "--host-uds=open") {
			t.Fatalf("runsc fallback not reported: %+v", st.SelfTest)
		}
		if f := findings(t, d.c, "sandbox.runsc_host_uds"); len(f) != 1 {
			t.Fatalf("doctor findings = %+v", f)
		}
	case "refused":
		if st.Available || !strings.Contains(st.Reason, "runsc") {
			t.Fatalf("runsc mode must refuse: available=%v reason=%q", st.Available, st.Reason)
		}
		err := d.c.Ensure(ctx, core.BoxSpec{Key: core.DerivedID(core.PrefixBox, "session:refused")})
		wantErr(t, err, core.ErrNoSandbox)
	default:
		t.Skipf("BOTHY_TEST_RUNSC_EXPECT=%q: want runsc, fallback or refused", expect)
	}
}
