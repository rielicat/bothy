package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type frame struct {
	stream int
	data   string
}

func demuxAll(t *testing.T, raw []byte) ([]frame, error) {
	t.Helper()
	var out []frame
	err := Demux(bytes.NewReader(raw), func(s int, p []byte) error {
		out = append(out, frame{s, string(p)})
		return nil
	})
	return out, err
}

func TestDemux(t *testing.T) {
	var raw []byte
	raw = AppendFrame(raw, Stdout, []byte("hello "))
	raw = AppendFrame(raw, Stdin, []byte("ignored"))
	raw = AppendFrame(raw, Stderr, []byte("oops"))
	raw = AppendFrame(raw, Stdout, nil)
	raw = AppendFrame(raw, Stdout, []byte("world"))
	got, err := demuxAll(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []frame{{1, "hello "}, {2, "oops"}, {1, "world"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDemuxSplitsLargeFrames(t *testing.T) {
	payload := bytes.Repeat([]byte("ab"), 50_000)
	got, err := demuxAll(t, AppendFrame(nil, Stdout, payload))
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, f := range got {
		if len(f.data) > demuxChunk {
			t.Fatalf("chunk of %d bytes", len(f.data))
		}
		joined.WriteString(f.data)
	}
	if joined.String() != string(payload) {
		t.Fatal("payload mangled")
	}
}

func TestDemuxErrors(t *testing.T) {
	good := AppendFrame(nil, Stdout, []byte("abcdef"))
	for name, raw := range map[string][]byte{
		"short header":    good[:5],
		"short payload":   good[:10],
		"bad stream":      append([]byte{9, 0, 0, 0, 0, 0, 0, 1}, 'x'),
		"nonzero padding": append([]byte{1, 1, 0, 0, 0, 0, 0, 1}, 'x'),
		"system error":    AppendFrame(nil, systemErr, []byte("exec failed")),
	} {
		if _, err := demuxAll(t, raw); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	stop := errors.New("stop")
	if err := Demux(bytes.NewReader(good), func(int, []byte) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("callback error not returned: %v", err)
	}
}

func FuzzDemux(f *testing.F) {
	f.Add(AppendFrame(AppendFrame(nil, Stdout, []byte("x")), Stderr, []byte("y")))
	f.Add([]byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{3, 0, 0, 0, 0, 0, 0, 2, 'h', 'i'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		total := 0
		_ = Demux(bytes.NewReader(raw), func(s int, p []byte) error {
			if s != Stdout && s != Stderr {
				t.Fatalf("stream %d delivered", s)
			}
			if len(p) > demuxChunk {
				t.Fatalf("chunk of %d", len(p))
			}
			total += len(p)
			return nil
		})
		if total > len(raw) {
			t.Fatalf("delivered %d bytes from %d input bytes", total, len(raw))
		}
	})
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"1.47", "1.45", 1}, {"1.45", "1.45", 0}, {"1.9", "1.45", -1}, {"2", "1.99", 1}, {"1.45.1", "1.45", 1}} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// fakeDocker serves h on a unix socket and returns a client for it.
func fakeDocker(t *testing.T, h http.Handler) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "eng")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	c, err := New("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsNonUnix(t *testing.T) {
	for _, ep := range []string{"tcp://1.2.3.4:2375", "relative.sock", ""} {
		if _, err := New(ep); err == nil {
			t.Errorf("New(%q) accepted", ep)
		}
	}
}

func TestNegotiatePinsVersion(t *testing.T) {
	for server, want := range map[string]string{"1.51": MaxAPI, "1.45": "1.45", "1.41": "1.41"} {
		var gotPath string
		c := fakeDocker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/_ping" {
				w.Header().Set("Api-Version", server)
				return
			}
			gotPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(Version{Version: "27.4.0"})
		}))
		v, err := c.Negotiate(context.Background())
		if err != nil || v != server {
			t.Fatalf("Negotiate = %q, %v", v, err)
		}
		if c.APIVersion() != want {
			t.Fatalf("server %s: pinned %s, want %s", server, c.APIVersion(), want)
		}
		if _, err := c.Version(context.Background()); err != nil {
			t.Fatal(err)
		}
		if gotPath != "/v"+want+"/version" {
			t.Fatalf("request path %q", gotPath)
		}
	}
}

func TestContainerLifecycle(t *testing.T) {
	var created map[string]any
	var listFilter string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "bothy-box_x" {
			http.Error(w, `{"message":"bad name"}`, 400)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&created)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"Id":"abc","Warnings":[]}`)
	})
	mux.HandleFunc("POST /containers/abc/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("POST /containers/abc/kill", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("signal") != "SIGKILL" {
			w.WriteHeader(400)
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("DELETE /containers/abc", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("force") != "1" {
			w.WriteHeader(400)
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("DELETE /containers/gone", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"No such container: gone"}`, 404)
	})
	mux.HandleFunc("DELETE /containers/racing", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"removal of container racing is already in progress"}`, 409)
	})
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		listFilter = r.URL.Query().Get("filters")
		fmt.Fprint(w, `[{"Id":"abc","Names":["/bothy-box_x"],"Labels":{"bothy.box":"1"},"State":"running"}]`)
	})
	mux.HandleFunc("GET /containers/missing/json", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"No such container"}`, 404)
	})
	c := fakeDocker(t, mux)
	ctx := context.Background()
	initFalse, pids := false, int64(64)
	id, err := c.ContainerCreate(ctx, "bothy-box_x", &ContainerConfig{Image: "img", HostConfig: &HostConfig{
		Init: &initFalse, PidsLimit: &pids, NanoCPUs: 5e8, NetworkMode: "none",
		Mounts: []Mount{{Type: "volume", Source: "v", Target: "/w", VolumeOptions: &VolumeOptions{Subpath: "ws/x", NoCopy: true}}}}})
	if err != nil || id != "abc" {
		t.Fatalf("create = %q, %v", id, err)
	}
	hc := created["HostConfig"].(map[string]any)
	if hc["Init"] != false || hc["NanoCpus"] != 5e8 || hc["PidsLimit"] != float64(64) {
		t.Fatalf("HostConfig on the wire: %v", hc)
	}
	vo := hc["Mounts"].([]any)[0].(map[string]any)["VolumeOptions"].(map[string]any)
	if vo["Subpath"] != "ws/x" || vo["NoCopy"] != true {
		t.Fatalf("VolumeOptions on the wire: %v", vo)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerKill(ctx, id, "SIGKILL"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"abc", "gone", "racing"} {
		if err := c.ContainerRemove(ctx, id); err != nil {
			t.Errorf("remove %s: %v", id, err)
		}
	}
	cs, err := c.ContainerList(ctx, []string{"bothy.box=1", "bothy.run_volume=bothy_run"})
	if err != nil || len(cs) != 1 || cs[0].State != "running" {
		t.Fatalf("list = %v, %v", cs, err)
	}
	if listFilter != `{"label":["bothy.box=1","bothy.run_volume=bothy_run"]}` {
		t.Fatalf("filters = %s", listFilter)
	}
	if _, err := c.ContainerInspect(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inspect missing: %v", err)
	}
	_, err = c.ContainerCreate(ctx, "other", &ContainerConfig{})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "bad name" {
		t.Fatalf("create error = %v", err)
	}
}

func TestImages(t *testing.T) {
	var pullQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /images/{ref...}", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "present") {
			fmt.Fprint(w, `{}`)
			return
		}
		http.Error(w, `{"message":"no such image"}`, 404)
	})
	mux.HandleFunc("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		pullQuery = r.URL.RawQuery
		if strings.Contains(r.URL.Query().Get("fromImage"), "broken") {
			fmt.Fprint(w, `{"status":"Pulling"}`+"\n"+`{"error":"manifest unknown"}`+"\n")
			return
		}
		fmt.Fprint(w, `{"status":"Pulling"}`+"\n"+`{"status":"Downloaded"}`+"\n")
	})
	c := fakeDocker(t, mux)
	ctx := context.Background()
	digest := "sha256:" + strings.Repeat("a", 64)
	if err := c.ImageInspect(ctx, "ghcr.io/x/present@"+digest); err != nil {
		t.Fatal(err)
	}
	if err := c.ImageInspect(ctx, "ghcr.io/x/absent@"+digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent image: %v", err)
	}
	if err := c.ImagePull(ctx, "ghcr.io/x/img@"+digest); err != nil {
		t.Fatal(err)
	}
	if pullQuery != "fromImage=ghcr.io%2Fx%2Fimg&tag="+strings.ReplaceAll(digest, ":", "%3A") {
		t.Fatalf("pull query %q", pullQuery)
	}
	if err := c.ImagePull(ctx, "ghcr.io/x/broken@"+digest); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("pull error in stream not reported: %v", err)
	}
	if err := c.ImagePull(ctx, "ghcr.io/x/img:latest"); err == nil {
		t.Fatal("pulled an unpinned image")
	}
}

func TestExecHijack(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /containers/abc/exec", func(w http.ResponseWriter, r *http.Request) {
		var cfg ExecConfig
		_ = json.NewDecoder(r.Body).Decode(&cfg)
		if cfg.User != "10001:10001" || !cfg.AttachStdin {
			http.Error(w, `{"message":"bad exec config"}`, 400)
			return
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{"Id":"ex1"}`)
	})
	mux.HandleFunc("POST /exec/ex1/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "tcp" {
			http.Error(w, "no upgrade", 400)
			return
		}
		if body, _ := io.ReadAll(r.Body); string(body) != `{"Detach":false,"Tty":false}` {
			http.Error(w, "bad start body", 400)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		in, _ := io.ReadAll(brw) // until the client half-closes stdin
		_, _ = conn.Write(AppendFrame(nil, Stdout, bytes.ToUpper(in)))
		_, _ = conn.Write(AppendFrame(nil, Stderr, []byte("done")))
	})
	mux.HandleFunc("GET /exec/ex1/json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Running":false,"ExitCode":3,"Pid":0}`)
	})
	c := fakeDocker(t, mux)
	ctx := context.Background()
	id, err := c.ExecCreate(ctx, "abc", ExecConfig{User: "10001:10001", AttachStdin: true, AttachStdout: true, Cmd: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.ExecStart(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Write([]byte("stdin data")); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if err := Demux(bufio.NewReader(s), func(st int, p []byte) error {
		if st == Stdout {
			out.Write(p)
		} else {
			errb.Write(p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "STDIN DATA" || errb.String() != "done" {
		t.Fatalf("stdout %q stderr %q", out.String(), errb.String())
	}
	ins, err := c.ExecInspect(ctx, id)
	if err != nil || ins.Running || ins.ExitCode != 3 {
		t.Fatalf("inspect = %+v, %v", ins, err)
	}
	if _, err := c.ExecStart(ctx, "missing"); err == nil {
		t.Fatal("ExecStart of a missing exec succeeded")
	}
}
