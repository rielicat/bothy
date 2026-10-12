package sandboxtest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

var ctx = context.Background()

func TestSandboxDefaults(t *testing.T) {
	s := &sandboxtest.Sandbox{UsedBytes: map[core.WorkspaceID]int64{"ws_1": 42}, QuotaBytes: 100}
	if st := s.Status(ctx); !st.Available || st.Backend != "fake" {
		t.Fatalf("Status = %+v", st)
	}
	if err := s.Ensure(ctx, core.BoxSpec{Key: "box_1"}); err != nil || len(s.Ensured) != 1 {
		t.Fatalf("Ensure = %v", err)
	}
	var out bytes.Buffer
	res, err := s.Exec(ctx, "box_1", core.ExecRequest{Argv: []string{"echo", "hi"}}, func(stream int, b []byte) {
		if stream == 1 {
			out.Write(b)
		}
	})
	if err != nil || res.Exit != 0 || string(res.Stdout) != "echo hi\n" || out.String() != "echo hi\n" {
		t.Fatalf("Exec = %+v %q, %v", res, out.String(), err)
	}
	if calls := s.ExecCalls(); len(calls) != 1 || calls[0].Key != "box_1" || calls[0].Req.Argv[1] != "hi" {
		t.Fatalf("ExecCalls = %+v", calls)
	}
	if used, quota, _ := s.Usage(ctx, "ws_1"); used != 42 || quota != 100 {
		t.Fatalf("Usage = %d/%d", used, quota)
	}
	if _, err := s.Stdio(ctx, "box_1", []string{"node"}, nil); !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("Stdio = %v", err)
	}
	a, b := net.Pipe()
	defer a.Close()
	s.StdioFunc = func(context.Context, string, []string, map[string]string) (io.ReadWriteCloser, error) { return b, nil }
	if rw, err := s.Stdio(ctx, "box_1", []string{"node"}, nil); err != nil || rw != b {
		t.Fatalf("Stdio with func = %v", err)
	}
	if err := s.Destroy(ctx, "box_1"); err != nil || s.Destroyed[0] != "box_1" {
		t.Fatalf("Destroy = %v", err)
	}
}

func TestSandboxExecFuncAndKill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &sandboxtest.Sandbox{ExecFunc: func(ctx context.Context, key string, req core.ExecRequest) (*core.ExecResult, error) {
			if req.Argv[0] == "fail" {
				return &core.ExecResult{Exit: 1, Stderr: []byte("nope")}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		var errOut string
		res, err := s.Exec(ctx, "box_1", core.ExecRequest{Argv: []string{"fail"}}, func(stream int, b []byte) {
			if stream == 2 {
				errOut = string(b)
			}
		})
		if err != nil || res.Exit != 1 || errOut != "nope" {
			t.Fatalf("ExecFunc result = %+v %q, %v", res, errOut, err)
		}

		type result struct {
			res *core.ExecResult
			err error
		}
		done := make(chan result)
		go func() {
			r, err := s.Exec(ctx, "box_1", core.ExecRequest{Argv: []string{"sleep", "600"}}, nil)
			done <- result{r, err}
		}()
		synctest.Wait()
		if err := s.Kill(ctx, "box_1"); err != nil {
			t.Fatal(err)
		}
		r := <-done
		if r.err != nil || !r.res.Killed || r.res.Exit != 137 {
			t.Fatalf("killed exec = %+v, %v", r.res, r.err)
		}
		if !slices.Equal(s.KilledKeys(), []string{"box_1"}) {
			t.Fatalf("Killed = %v", s.KilledKeys())
		}

		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Exec(cctx, "box_2", core.ExecRequest{Argv: []string{"sleep"}}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled exec = %v", err)
		}
	})
}

func TestSandboxUnavailable(t *testing.T) {
	s := &sandboxtest.Sandbox{StatusV: core.SandboxStatus{Backend: "none", Reason: "no docker"}}
	if st := s.Status(ctx); st.Available || st.Reason != "no docker" {
		t.Fatalf("Status = %+v", st)
	}
	if _, _, err := s.Usage(ctx, "ws_1"); err != nil {
		t.Fatalf("Usage = %v", err)
	}
	_, execErr := s.Exec(ctx, "k", core.ExecRequest{}, nil)
	_, stdioErr := s.Stdio(ctx, "k", nil, nil)
	for name, err := range map[string]error{
		"Ensure": s.Ensure(ctx, core.BoxSpec{}), "Exec": execErr, "Stdio": stdioErr,
		"Kill": s.Kill(ctx, "k"), "Destroy": s.Destroy(ctx, "k"),
	} {
		if !errors.Is(err, core.ErrNoSandbox) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	if len(s.Killed) != 1 || len(s.Execs) != 0 {
		t.Fatal("Kill must be recorded, Exec must not")
	}

	var seen []core.SandboxStatus
	s.Watch(func(st core.SandboxStatus) { seen = append(seen, st) })
	s.SetStatus(core.SandboxStatus{})
	if len(seen) != 1 || !seen[0].Available || s.Ensure(ctx, core.BoxSpec{}) != nil {
		t.Fatalf("SetStatus = %+v", seen)
	}
}

func TestEgress(t *testing.T) {
	e := &sandboxtest.Egress{}
	sub, err := e.OpenBox(ctx, "box_1", core.BoxEgress{Session: "ses_1"})
	again, _ := e.OpenBox(ctx, "box_1", core.BoxEgress{Session: "ses_1"})
	if err != nil || !strings.HasPrefix(sub, "egress/") || sub != again || e.Opened["box_1"].Session != "ses_1" {
		t.Fatalf("OpenBox = %q, %v", sub, err)
	}
	e.Bind("box_1", core.EgressBinding{Run: "run_1"})
	e.CloseBox("box_1")
	if len(e.Bound) != 1 || e.Bound[0].Run != "run_1" || e.Closed[0] != "box_1" {
		t.Fatal("Bind/CloseBox not recorded")
	}
	if e.TakeActivity("box_1") {
		t.Fatal("activity without traffic")
	}
	e.MarkActivity("box_1")
	if !e.TakeActivity("box_1") || e.TakeActivity("box_1") {
		t.Fatal("TakeActivity must report once")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	if _, err := e.PublicClient("ses_1", "usr_a").Get(srv.URL); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("client without Client set = %v", err)
	}
	e.Client = srv.Client()
	resp, err := e.SystemClient(core.SystemDest{Name: "provider:mock"}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(e.Dests) != 1 || e.Dests[0].Name != "provider:mock" || e.PublicClient("", "") != srv.Client() {
		t.Fatalf("Dests = %+v", e.Dests)
	}
}
