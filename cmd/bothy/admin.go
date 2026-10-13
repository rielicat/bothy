package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/bothyhq/bothy/internal/audit"
	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/doctor"
)

var errUsage = errors.New("usage")

// doctorCmd builds the app without starting its loops, runs the checks and
// prints them. security-audit keeps security findings and can apply the
// automatic fixes, after which it checks again.
func doctorCmd(ctx context.Context, args []string, security bool) (int, error) {
	name := "doctor"
	if security {
		name = "security-audit"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the findings as JSON")
	var fix bool
	if security {
		fs.BoolVar(&fix, "fix", false, "apply the automatic fixes, then check again")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, nil
	}
	a, err := build(ctx)
	if err != nil {
		return 0, err
	}
	defer a.close()
	findings := a.doctor.Run(ctx, security)
	if fix {
		for _, id := range a.doctor.Fix(ctx, findings) {
			fmt.Fprintln(os.Stderr, "fixed", id)
		}
		findings = a.doctor.Run(ctx, security)
	}
	return doctor.Print(os.Stdout, findings, *asJSON), nil
}

// adminCmd runs the offline admin commands on the module APIs. Only the
// pieces each one needs are opened, never the gateway's loops.
func adminCmd(ctx context.Context, cmd string, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	err := runAdmin(ctx, cmd, sub, args)
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	return err
}

func runAdmin(ctx context.Context, cmd, sub string, args []string) error {
	switch {
	case cmd == "config":
		return configCmd(ctx, sub, args)
	case cmd == "audit" && sub == "archive":
		return auditArchive(ctx, args[1:])
	}
	a, err := buildBase(ctx)
	if err != nil {
		return err
	}
	defer a.close()
	switch {
	case cmd == "setup-token" && len(args) == 0:
		tok, err := a.auth.RotateSetupToken(ctx)
		if err != nil {
			return err
		}
		printSetupBanner(a.cfg.Current(), tok)
		fmt.Println(tok)
		return nil
	case cmd == "user" && sub == "list" && len(args) == 1:
		return listUsers(ctx, a)
	case cmd == "user" && sub == "reset-password" && len(args) == 2:
		return resetPassword(ctx, a, args[1], os.Stdin)
	case cmd == "vault" && sub == "status" && len(args) == 1:
		return printJSON(a.vault.Status())
	case cmd == "vault" && sub == "rotate-kek" && len(args) == 1:
		if err := a.vault.RotateKEK(ctx, ""); err != nil {
			return err
		}
		fmt.Printf("Vault keys rotated (KEK %s, source %s).\n", a.vault.Status().KEKID, a.vault.Status().KEKSource)
		return nil
	case cmd == "audit" && sub == "verify" && len(args) == 1:
		bad, err := a.audit.Verify(ctx)
		if err != nil {
			return err
		}
		if bad != 0 {
			return fmt.Errorf("audit chain broken at row %d", bad)
		}
		fmt.Println("Audit chain intact.")
		return nil
	case cmd == "backup" && len(args) == 0:
		p, err := a.db.Backup(ctx, filepath.Join(a.dirs.Data, "backups"))
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	}
	return errUsage
}

func listUsers(ctx context.Context, a *app) error {
	us, err := a.db.Users().List(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tROLE\tSTATUS\tID")
	for _, u := range us {
		status := "active"
		if u.Disabled {
			status = "disabled"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", u.Username, u.Role, status, u.ID)
	}
	return w.Flush()
}

// resetPassword reads the new password from the first line of in, so it
// never appears in argv or the shell history.
func resetPassword(ctx context.Context, a *app, username string, in io.Reader) error {
	u, err := a.db.Users().ByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("user %q: %w", username, err)
	}
	if f, ok := in.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintf(os.Stderr, "New password for %s: ", u.Username)
		}
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return errors.New("no password given on standard input")
	}
	if err := a.auth.ResetPassword(ctx, u.ID, pw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Password reset for %s.\n", u.Username)
	return nil
}

// auditArchive runs with the gateway stopped and opens the database itself.
func auditArchive(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("audit archive", flag.ContinueOnError)
	beforeS := fs.String("before", "", "archive rows older than this date (YYYY-MM-DD or RFC 3339)")
	if err := fs.Parse(args); err != nil || *beforeS == "" || fs.NArg() != 0 {
		return errUsage
	}
	before, err := parseDate(*beforeS)
	if err != nil {
		return err
	}
	m, err := loadConfig(ctx, "")
	if err != nil {
		return err
	}
	dirs := config.ResolveDirs(m.Current())
	res, err := audit.Archive(ctx, filepath.Join(dirs.Data, "bothy.db"), before)
	if err != nil {
		return err
	}
	if res.Count == 0 {
		fmt.Println("No audit rows before", before.Format(time.RFC3339))
		return nil
	}
	fmt.Printf("Archived %d audit rows (%d..%d) to %s; anchor row %d.\n", res.Count, res.FromID, res.ToID, res.File, res.AnchorID)
	return nil
}

func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--before %q: want YYYY-MM-DD or RFC 3339", s)
	}
	return t, nil
}

func configCmd(ctx context.Context, sub string, args []string) error {
	switch {
	case sub == "defaults" && len(args) == 1:
		out, err := yaml.Marshal(config.Default())
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(out)
		return err
	case sub == "effective" && len(args) == 1:
		m, err := loadConfig(ctx, "")
		if err != nil {
			return err
		}
		return printJSON(m.Effective())
	case sub == "check" && len(args) <= 2:
		file := ""
		if len(args) == 2 {
			file = args[1]
		}
		m, err := loadConfig(ctx, file)
		if err != nil {
			return err
		}
		eff := m.Effective()
		for _, e := range eff.Errors {
			where := string(e.Source.Layer)
			if e.Source.File != "" {
				where = fmt.Sprintf("%s:%d", e.Source.File, e.Source.Line)
			} else if e.Source.EnvVar != "" {
				where = e.Source.EnvVar
			}
			fmt.Fprintf(os.Stderr, "%s: %s: %s\n", where, cmp.Or(e.Path, "<file>"), e.Message)
		}
		if eff.SafeMode || len(eff.Errors) > 0 {
			return fmt.Errorf("config check: %d problem(s)", max(len(eff.Errors), 1))
		}
		fmt.Println("Config OK.")
		return nil
	}
	return errUsage
}

// loadConfig loads the layered config the way serve does; file overrides
// $BOTHY_CONFIG when set.
func loadConfig(ctx context.Context, file string) (*config.Manager, error) {
	if file == "" {
		file = os.Getenv("BOTHY_CONFIG")
	}
	return config.Load(ctx, config.Options{DataDir: envOr("BOTHY_STORAGE__DATA_DIR", "/data"), File: file, Env: os.Environ})
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
