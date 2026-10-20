package config

import (
	"context"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// SystemKeyMessage is why an MCP server or model provider may not send a key.
const SystemKeyMessage = "this key belongs to Bothy and can't be sent to other services"

// SystemKeys is what an MCP server may never send: the names
// core.IsSystemSecret knows, and whatever the settings holding Bothy's own
// keys reference, under any name or as an aws-sm secret.
type SystemKeys struct {
	names map[string]bool // core.FoldSecretName
	aws   []string        // AWSSecretRef.SecretID
}

// SystemSecretRefs are the values of the settings that hold Bothy's own
// keys: its chat apps' tokens and the sign-in client secrets.
func (c *Config) SystemSecretRefs() []string {
	ch, si := c.Channels, c.Auth.SignIn
	return []string{ch.Telegram.Token, ch.Discord.Token, ch.Slack.AppToken, ch.Slack.BotToken, si.Google.ClientSecret, si.GitHub.ClientSecret}
}

// NewSystemKeys collects the keys refs (SystemSecretRefs, and the GitHub
// connections' core.GitHubSecretRefs) reference.
func NewSystemKeys(refs ...string) SystemKeys {
	k := SystemKeys{names: map[string]bool{}}
	for _, r := range refs {
		for _, n := range SecretNames(r) {
			k.names[core.FoldSecretName(n)] = true
		}
		aws, _ := FindAWSSecretRefs(r)
		for _, a := range aws {
			k.aws = append(k.aws, a.Ref.SecretID)
		}
	}
	return k
}

// Sends reports whether a header value or address references one of
// Bothy's own keys.
func (k SystemKeys) Sends(v string) bool {
	for _, n := range SecretNames(v) {
		if core.IsSystemSecret(n) || k.names[core.FoldSecretName(n)] {
			return true
		}
	}
	aws, _ := FindAWSSecretRefs(v)
	for _, a := range aws {
		if slices.ContainsFunc(k.aws, func(id string) bool { return sameAWSSecret(a.Ref.SecretID, id) }) {
			return true
		}
	}
	return false
}

// sameAWSSecret compares two secret ids that may name one secret, by name
// or by ARN; its key, region and profile don't matter.
func sameAWSSecret(a, b string) bool {
	na, aARN := awsSecretName(a)
	nb, bARN := awsSecretName(b)
	return na == nb || aARN && trimARNSuffix(na) == nb || bARN && trimARNSuffix(nb) == na
}

func awsSecretName(id string) (name string, arn bool) {
	const marker = ":secret:"
	if i := strings.Index(id, marker); strings.HasPrefix(id, "arn:") && i >= 0 {
		return id[i+len(marker):], true
	}
	return id, false
}

// trimARNSuffix drops the "-" and six random characters Secrets Manager
// appends to a secret's name in its full ARN.
func trimARNSuffix(n string) string {
	if i := len(n) - 7; i > 0 && n[i] == '-' {
		return n[:i]
	}
	return n
}

type systemRefsKey struct{}

// WithSystemRefs has Set and Reset on ctx also treat refs, references to
// Bothy's own keys kept outside the configuration (the GitHub connections'),
// as SystemKeys.
func WithSystemRefs(ctx context.Context, refs []string) context.Context {
	return context.WithValue(ctx, systemRefsKey{}, refs)
}

func systemRefsOf(ctx context.Context) []string {
	r, _ := ctx.Value(systemRefsKey{}).([]string)
	return r
}

// systemKeyErrors refuses a change that has an MCP server send one of
// Bothy's own keys, in a header or its address, or a model provider send one
// as its API key or a header. It runs on writes only, not
// in validate: a value already in effect is left as it is, so a file that
// had one still loads and the server's status says why it can't start
// (SPEC §2.11) rather than the entry vanishing.
func systemKeyErrors(ctx context.Context, old, next *Config, src Source) FieldErrors {
	keys := NewSystemKeys(append(next.SystemSecretRefs(), systemRefsOf(ctx)...)...)
	var out FieldErrors
	for _, s := range next.MCP.Servers {
		var was MCPServer
		if i := slices.IndexFunc(old.MCP.Servers, func(o MCPServer) bool { return o.Name == s.Name }); i >= 0 {
			was = old.MCP.Servers[i]
		}
		base := "mcp.servers." + s.Name
		for _, h := range keysOf(s.Headers) {
			if v := s.Headers[h]; v != was.Headers[h] && keys.Sends(v) {
				out = append(out, newIssue(base+".headers."+h, "", src, SystemKeyMessage).FieldError)
			}
		}
		if s.URL != was.URL && keys.Sends(s.URL) {
			out = append(out, newIssue(base+".url", "", src, SystemKeyMessage).FieldError)
		}
	}
	for _, p := range next.Providers {
		var was Provider
		if i := slices.IndexFunc(old.Providers, func(o Provider) bool { return o.ID == p.ID }); i >= 0 {
			was = old.Providers[i]
		}
		base := "providers." + p.ID
		if p.APIKey != was.APIKey && keys.Sends(p.APIKey) {
			out = append(out, newIssue(base+".api_key", "", src, SystemKeyMessage).FieldError)
		}
		for _, h := range keysOf(p.Headers) {
			if v := p.Headers[h]; v != was.Headers[h] && keys.Sends(v) {
				out = append(out, newIssue(base+".headers."+h, "", src, SystemKeyMessage).FieldError)
			}
		}
	}
	return out
}
