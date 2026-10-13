//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(file))
}

func writeExec(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeRelease lays out what install.sh downloads for version 0.9.0 on
// linux/amd64, with checksums files that match.
func fakeRelease(t *testing.T, root string) string {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	bin := []byte("#!/bin/sh\necho fake bothy\n")
	if err := tw.WriteHeader(&tar.Header{Name: "bothy", Mode: 0o755, Size: int64(len(bin))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(bin)
	_ = tw.Close()
	_ = gz.Close()
	files := map[string][]byte{"bothy_0.9.0_linux_amd64.tar.gz": buf.Bytes()}
	for name, src := range map[string]string{"compose.yaml": "deploy/compose.yaml",
		"searxng-settings.yml": "deploy/searxng/settings.yml", "env.example": "deploy/.env.example"} {
		b, err := os.ReadFile(filepath.Join(root, src))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = b
	}
	sums := func(names ...string) []byte {
		var s strings.Builder
		for _, n := range names {
			h := sha256.Sum256(files[n])
			fmt.Fprintf(&s, "%s  %s\n", hex.EncodeToString(h[:]), n)
		}
		return []byte(s.String())
	}
	files["checksums.txt"] = sums("bothy_0.9.0_linux_amd64.tar.gz")
	files["deploy-checksums.txt"] = sums("compose.yaml", "searxng-settings.yml", "env.example")
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestInstallComposeEnv runs deploy/install.sh --compose against a fake
// release and a fake docker CLI, and checks the Docker values it writes to
// .env: the socket's host path and its group as containers see it.
func TestInstallComposeEnv(t *testing.T) {
	root := repoRoot(t)
	release := fakeRelease(t, root)
	fake := t.TempDir()
	writeExec(t, filepath.Join(fake, "uname"), `case "$1" in -s) echo Linux ;; -m) echo x86_64 ;; esac`+"\n")
	writeExec(t, filepath.Join(fake, "curl"), `out= url=
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out=$2; shift 2 ;;
	--proto) shift 2 ;;
	-*) shift ;;
	*) url=$1; shift ;;
	esac
done
cp "$FAKE_RELEASE/${url##*/}" "$out"
`)
	writeExec(t, filepath.Join(fake, "docker"), `case "$*" in
"context inspect"*) [ -n "$FAKE_DOCKER_ENDPOINT" ] && echo "$FAKE_DOCKER_ENDPOINT" ;;
*OperatingSystem*) echo "$FAKE_DOCKER_OS" ;;
*SecurityOptions*) echo "$FAKE_DOCKER_SECOPTS" ;;
esac
`)

	// Unix socket paths are limited to about 104 bytes.
	xdg, err := os.MkdirTemp("", "xdg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(xdg) })
	sockets := map[string]string{}
	for _, name := range []string{"rootless", "rootful"} {
		dir := filepath.Join(xdg, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "docker.sock")
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		sockets[name] = p
	}
	fi, err := os.Stat(sockets["rootful"])
	if err != nil {
		t.Fatal(err)
	}
	gid := reflect.ValueOf(fi.Sys()).Elem().FieldByName("Gid") // syscall.Stat_t, which Windows lacks
	if !gid.IsValid() {
		t.Skip("install.sh targets Linux and macOS")
	}
	rootfulGID := fmt.Sprint(gid.Interface())

	cases := []struct {
		name     string
		env      []string
		compose  bool
		wantSock string
		wantGID  string
	}{
		// docker context "rootless" points at $XDG_RUNTIME_DIR/docker.sock.
		{"rootless", []string{"XDG_RUNTIME_DIR=" + filepath.Join(xdg, "rootless"), "FAKE_DOCKER_ENDPOINT=unix://" + sockets["rootless"],
			"FAKE_DOCKER_OS=Ubuntu 24.04", "FAKE_DOCKER_SECOPTS=[name=seccomp,profile=builtin name=rootless name=cgroupns]"},
			true, sockets["rootless"], "0"},
		{"rootful", []string{"DOCKER_HOST=unix://" + sockets["rootful"], "FAKE_DOCKER_OS=Ubuntu 24.04",
			"FAKE_DOCKER_SECOPTS=[name=apparmor name=seccomp,profile=builtin]"}, true, sockets["rootful"], rootfulGID},
		// Desktop's Linux context points at ~/.docker/desktop/docker.sock, but
		// containers mounting /var/run/docker.sock get the VM's root:root socket.
		{"desktop on linux", []string{"FAKE_DOCKER_ENDPOINT=unix://" + sockets["rootful"], "FAKE_DOCKER_OS=Docker Desktop"},
			true, "/var/run/docker.sock", "0"},
		{"binary only", nil, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			args := []string{filepath.Join(root, "deploy", "install.sh"), "--version", "0.9.0", "--dir", filepath.Join(home, "bin"), "--skip-signature"}
			if c.compose {
				args = append(args, "--compose", filepath.Join(home, "bothy"))
			}
			cmd := exec.CommandContext(context.Background(), "sh", args...)
			cmd.Env = append([]string{"PATH=" + fake + ":/usr/bin:/bin", "HOME=" + home, "FAKE_RELEASE=" + release}, c.env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install.sh: %v\n%s", err, out)
			}
			if !c.compose {
				if !strings.Contains(string(out), "BOTHY_STORAGE__DATA_DIR=") || !strings.Contains(string(out), "bothy serve") {
					t.Fatalf("no run instructions for the native binary:\n%s", out)
				}
				return
			}
			env, err := os.ReadFile(filepath.Join(home, "bothy", ".env"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"DOCKER_SOCK=" + c.wantSock + "\n", "DOCKER_GID=" + c.wantGID + "\n"} {
				if !strings.Contains(string(env), "\n"+want) {
					t.Errorf(".env lacks %q:\n%s\ninstall.sh said:\n%s", strings.TrimSpace(want), env, out)
				}
			}
		})
	}
}

// uncommentKEK enables the optional Docker secret KEK the way compose.yaml's
// comments describe: the bothy service's secrets line and the top-level block.
func uncommentKEK(b []byte) []byte {
	re := regexp.MustCompile(`(?m)^(\s*)# (secrets:.*|  bothy_kek:.*|    file: .*)$`)
	return re.ReplaceAll(b, []byte("$1$2"))
}

func TestComposeKEKSecretUncomments(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	on := uncommentKEK(b)
	var project struct {
		Services map[string]struct {
			Secrets []string `yaml:"secrets"`
		} `yaml:"services"`
		Secrets map[string]struct {
			File string `yaml:"file"`
		} `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(on, &project); err != nil {
		t.Fatalf("uncommented compose.yaml does not parse: %v", err)
	}
	if !slices.Contains(project.Services["bothy"].Secrets, "bothy_kek") {
		t.Fatalf("bothy service secrets = %v", project.Services["bothy"].Secrets)
	}
	for svc, s := range project.Services {
		for _, name := range s.Secrets {
			if project.Secrets[name].File == "" {
				t.Errorf("service %s refers to undefined secret %s", svc, name)
			}
		}
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	dir := t.TempDir()
	for name, data := range map[string][]byte{"compose.yaml": on, "bothy_kek": nil} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(context.Background(), "docker", "compose", "-f", filepath.Join(dir, "compose.yaml"), "config", "-q")
	if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "is not a docker command") {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
}

// TestReleaseGate: the tag job builds a draft; `latest` and the Homebrew
// cask move only when a maintainer publishes it.
func TestReleaseGate(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var gr struct {
		Dockers []struct {
			ID   string   `yaml:"id"`
			Tags []string `yaml:"tags"`
		} `yaml:"dockers_v2"`
		Casks []struct {
			SkipUpload string `yaml:"skip_upload"`
		} `yaml:"homebrew_casks"`
		Release struct {
			Draft bool `yaml:"draft"`
		} `yaml:"release"`
	}
	if err := yaml.Unmarshal(b, &gr); err != nil {
		t.Fatal(err)
	}
	if !gr.Release.Draft || len(gr.Dockers) == 0 || len(gr.Casks) == 0 {
		t.Fatalf("unexpected .goreleaser.yaml shape: %+v", gr)
	}
	for _, d := range gr.Dockers {
		for _, tag := range d.Tags {
			if strings.Contains(tag, "latest") {
				t.Errorf("image %s pushes %q from the tag job, before the draft is reviewed", d.ID, tag)
			}
		}
	}
	for _, c := range gr.Casks {
		if c.SkipUpload != "true" {
			t.Errorf("cask skip_upload %q: the tag job would commit it to the tap", c.SkipUpload)
		}
	}
	w, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release-publish.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		On struct {
			Release struct {
				Types []string `yaml:"types"`
			} `yaml:"release"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(w, &wf); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(wf.On.Release.Types, "released") {
		t.Errorf("release-publish.yml runs on %v", wf.On.Release.Types)
	}
	for _, want := range []string{"imagetools create", "bothy-sandbox-node", "homebrew-tap/contents/Casks/bothy.rb"} {
		if !strings.Contains(string(w), want) {
			t.Errorf("release-publish.yml lacks %q", want)
		}
	}
}
