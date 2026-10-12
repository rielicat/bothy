// Command cooldown enforces the dependency cooldown (SPEC §10, row 12): every
// Go module or npm package version added or changed since -base must have been
// published at least -days ago, per proxy.golang.org and the npm registry.
// Without -base it checks every pinned version.
//
//	cd tools && go run ./cooldown -root .. -base origin/main
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/module"
)

type dep struct {
	eco, file, name, version string
}

func main() {
	root := flag.String("root", "..", "repository root")
	base := flag.String("base", "", "git revision to diff against (empty: check every pinned version)")
	days := flag.Int("days", 14, "minimum age in days")
	allow := flag.String("allow", "", "comma-separated name@version exceptions (security fixes approved by the lead)")
	flag.Parse()
	if err := run(*root, *base, time.Duration(*days)*24*time.Hour, *allow); err != nil {
		fmt.Fprintln(os.Stderr, "cooldown:", err)
		os.Exit(1)
	}
}

func run(root, base string, minAge time.Duration, allow string) error {
	allowed := map[string]bool{}
	for _, a := range strings.Split(allow, ",") {
		if a = strings.TrimSpace(a); a != "" {
			allowed[a] = true
		}
	}
	var deps []dep
	for _, f := range []struct{ eco, path string }{
		{"go", "go.mod"}, {"go", "tools/go.mod"}, {"npm", "web/package-lock.json"},
	} {
		cur, err := os.ReadFile(filepath.Join(root, f.path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var old []byte
		if base != "" {
			old = gitShow(root, base, f.path)
		}
		parse := goRequires
		if f.eco == "npm" {
			parse = npmLocked
		}
		now, err := parse(cur)
		if err != nil {
			return fmt.Errorf("%s: %w", f.path, err)
		}
		before, err := parse(old)
		if err != nil {
			return fmt.Errorf("%s at %s: %w", f.path, base, err)
		}
		for name, v := range now {
			if before[name] != v && !allowed[name+"@"+v] {
				deps = append(deps, dep{f.eco, f.path, name, v})
			}
		}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].file+deps[i].name < deps[j].file+deps[j].name })

	client := &http.Client{Timeout: 30 * time.Second}
	type result struct {
		d   dep
		at  time.Time
		err error
	}
	results := make([]result, len(deps))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, d := range deps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			at, err := published(client, d)
			results[i] = result{d, at, err}
		}()
	}
	wg.Wait()

	var bad []string
	for _, r := range results {
		switch age := time.Since(r.at); {
		case r.err != nil:
			bad = append(bad, fmt.Sprintf("%s: %s@%s: %v", r.d.file, r.d.name, r.d.version, r.err))
		case age < minAge:
			bad = append(bad, fmt.Sprintf("%s: %s@%s published %s (%.0f days ago)", r.d.file, r.d.name, r.d.version,
				r.at.Format("2006-01-02"), age.Hours()/24))
		}
	}
	fmt.Printf("cooldown: %d new or changed version(s) checked\n", len(deps))
	if len(bad) > 0 {
		return fmt.Errorf("versions younger than %.0f days or unverifiable:\n  %s", minAge.Hours()/24, strings.Join(bad, "\n  "))
	}
	return nil
}

func gitShow(root, rev, path string) []byte {
	out, err := exec.CommandContext(context.Background(), "git", "-C", root, "show", rev+":"+path).Output()
	if err != nil {
		return nil // file absent at rev: everything in it is new
	}
	return out
}

// goRequires reads the require directives of a go.mod (single-line and block).
func goRequires(data []byte) (map[string]string, error) {
	reqs := map[string]string{}
	inBlock := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "//")
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case inBlock && f[0] == ")":
			inBlock = false
		case inBlock && len(f) >= 2:
			reqs[f[0]] = f[1]
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			inBlock = true
		case f[0] == "require" && len(f) >= 3:
			reqs[f[1]] = f[2]
		}
	}
	return reqs, sc.Err()
}

// npmLocked reads every installed package version from a lockfile v2/v3.
func npmLocked(data []byte) (map[string]string, error) {
	pkgs := map[string]string{}
	if len(data) == 0 {
		return pkgs, nil
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Link    bool   `json:"link"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	for path, p := range lock.Packages {
		i := strings.LastIndex(path, "node_modules/")
		if path == "" || p.Link || i < 0 {
			continue
		}
		pkgs[path[i+len("node_modules/"):]] = p.Version
	}
	return pkgs, nil
}

var (
	npmMu    sync.Mutex
	npmTimes = map[string]map[string]time.Time{}
)

func published(c *http.Client, d dep) (time.Time, error) {
	if d.eco == "go" {
		p, err := module.EscapePath(d.name)
		if err != nil {
			return time.Time{}, err
		}
		v, err := module.EscapeVersion(d.version)
		if err != nil {
			return time.Time{}, err
		}
		var info struct{ Time time.Time }
		err = getJSON(c, "https://proxy.golang.org/"+p+"/@v/"+v+".info", &info)
		return info.Time, err
	}
	npmMu.Lock()
	times, ok := npmTimes[d.name]
	npmMu.Unlock()
	if !ok {
		var doc struct {
			Time map[string]time.Time `json:"time"`
		}
		if err := getJSON(c, "https://registry.npmjs.org/"+url.PathEscape(d.name), &doc); err != nil {
			return time.Time{}, err
		}
		times = doc.Time
		npmMu.Lock()
		npmTimes[d.name] = times
		npmMu.Unlock()
	}
	at, ok := times[d.version]
	if !ok {
		return time.Time{}, errors.New("version not in the registry")
	}
	return at, nil
}

func getJSON(c *http.Client, u string, v any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
