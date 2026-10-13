package sandboxd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

// Config comes only from BOTHY_SBX_* environment variables (SPEC §2.2).
type Config struct {
	Engine        string            // BOTHY_SBX_ENGINE, unix:///var/run/docker.sock
	WorkVolume    string            // BOTHY_SBX_WORK_VOLUME, engine name of the work volume
	RunVolume     string            // BOTHY_SBX_RUN_VOLUME
	Images        map[string]string // BOTHY_SBX_IMAGES, profile -> digest-pinned ref
	Runtime       string            // BOTHY_SBX_RUNTIME: auto | runsc | runc
	MaxBoxes      int               // BOTHY_SBX_MAX_BOXES
	GatewayUID    int               // BOTHY_SBX_GATEWAY_UID, the only peer uid served
	Idle          time.Duration     // BOTHY_SBX_IDLE
	MaxLifetime   time.Duration     // BOTHY_SBX_MAX_LIFETIME
	MaxExec       time.Duration     // BOTHY_SBX_MAX_EXEC, cap on ExecReq.TimeoutMS
	Socket        string            // BOTHY_SBX_SOCKET, /run/bothy/sandboxd.sock
	RunDir        string            // BOTHY_SBX_RUN_DIR, where the run volume is mounted here
	SelfTestEvery time.Duration     // BOTHY_SBX_SELFTEST_EVERY
}

var (
	volumeRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	// Registry digest pins, or a local image id (content-addressed as well;
	// such images are never pulled).
	pinnedRefRE = regexp.MustCompile(`^([a-z0-9][a-z0-9._:/-]{0,254}@)?sha256:[0-9a-f]{64}$`)
)

// ParseConfig reads the BOTHY_SBX_* variables of environ.
func ParseConfig(environ []string) (Config, error) {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "BOTHY_SBX_") {
			env[k] = v
		}
	}
	get := func(k, def string) string {
		if v := env[k]; v != "" {
			return v
		}
		return def
	}
	c := Config{
		Engine:     get("BOTHY_SBX_ENGINE", "unix:///var/run/docker.sock"),
		WorkVolume: get("BOTHY_SBX_WORK_VOLUME", "bothy_work"),
		RunVolume:  get("BOTHY_SBX_RUN_VOLUME", "bothy_run"),
		Runtime:    get("BOTHY_SBX_RUNTIME", "auto"),
		Socket:     get("BOTHY_SBX_SOCKET", "/run/bothy/sandboxd.sock"),
	}
	c.RunDir = get("BOTHY_SBX_RUN_DIR", filepath.Dir(c.Socket))
	var err error
	if c.MaxBoxes, err = intEnv(get("BOTHY_SBX_MAX_BOXES", "8"), 1, 256); err != nil {
		return c, fmt.Errorf("BOTHY_SBX_MAX_BOXES: %w", err)
	}
	if c.GatewayUID, err = intEnv(get("BOTHY_SBX_GATEWAY_UID", "65532"), 0, 1<<31-1); err != nil {
		return c, fmt.Errorf("BOTHY_SBX_GATEWAY_UID: %w", err)
	}
	for _, d := range []struct {
		key, def string
		dst      *time.Duration
	}{
		{"BOTHY_SBX_IDLE", "10m", &c.Idle},
		{"BOTHY_SBX_MAX_LIFETIME", "4h", &c.MaxLifetime},
		{"BOTHY_SBX_MAX_EXEC", "30m", &c.MaxExec},
		{"BOTHY_SBX_SELFTEST_EVERY", "10m", &c.SelfTestEvery},
	} {
		v, err := time.ParseDuration(get(d.key, d.def))
		if err != nil || v <= 0 {
			return c, fmt.Errorf("%s: want a positive duration such as %s", d.key, d.def)
		}
		*d.dst = v
	}
	if !slices.Contains([]string{"auto", "runsc", "runc"}, c.Runtime) {
		return c, fmt.Errorf("BOTHY_SBX_RUNTIME: want auto, runsc or runc, got %q", c.Runtime)
	}
	for _, v := range []struct{ key, val string }{{"BOTHY_SBX_WORK_VOLUME", c.WorkVolume}, {"BOTHY_SBX_RUN_VOLUME", c.RunVolume}} {
		if !volumeRE.MatchString(v.val) {
			return c, fmt.Errorf("%s: %q is not a volume name", v.key, v.val)
		}
	}
	if !filepath.IsAbs(c.Socket) || !filepath.IsAbs(c.RunDir) {
		return c, fmt.Errorf("BOTHY_SBX_SOCKET and BOTHY_SBX_RUN_DIR must be absolute paths")
	}
	if c.Images, err = parseImages(env["BOTHY_SBX_IMAGES"]); err != nil {
		return c, fmt.Errorf("BOTHY_SBX_IMAGES: %w", err)
	}
	return c, nil
}

func intEnv(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("want an integer in [%d, %d], got %q", lo, hi, s)
	}
	return n, nil
}

// parseImages reads "base=<ref>,node=<ref>". Every ref must be pinned by
// digest: the image is the box's whole userland.
func parseImages(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("at least one profile=image@sha256:<digest> entry is required")
	}
	out := map[string]string{}
	for item := range strings.SplitSeq(s, ",") {
		item = strings.TrimSpace(item)
		k, ref, ok := strings.Cut(item, "=")
		if !ok || !proto.ValidProfile(k) {
			return nil, fmt.Errorf("entry %q: want profile=image@sha256:<digest>", item)
		}
		if !pinnedRefRE.MatchString(ref) {
			return nil, fmt.Errorf("profile %s: image %q is not pinned by sha256 digest", k, ref)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("profile %s listed twice", k)
		}
		out[k] = ref
	}
	return out, nil
}

// canaryProfile is "base" when configured, else the first profile by name.
func (c Config) canaryProfile() string {
	if _, ok := c.Images["base"]; ok {
		return "base"
	}
	keys := make([]string, 0, len(c.Images))
	for k := range c.Images {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys[0]
}
