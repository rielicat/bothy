package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/bedrock"
	"github.com/rielicat/bothy/internal/provider/wire"
)

const (
	// awsDetectTimeout bounds Detect's credential resolution (an SSO portal
	// call, IMDS) and model listing when the environment suggests AWS
	// access; without any sign of it only the instance metadata service is
	// tried, within the local detection timeout.
	awsDetectTimeout = 4 * time.Second
	awsCheckTimeout  = 15 * time.Second
	// bedrockFirstMax replaces the probe's one-token completion: models that
	// think by default refuse a smaller output budget.
	bedrockFirstMax = 32
)

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// awsChain builds the AWS default credential chain for one provider: AWS
// credential services through an egress system client limited to the hosts
// the configuration needs, instance and container credentials through
// egress's credentials-only client.
func (f *Facade) awsChain(name, region, profile string, imdsRegion bool) *bedrock.Chain {
	env := envMap(f.d.Env())
	rel, full := env["AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"], env["AWS_CONTAINER_CREDENTIALS_FULL_URI"]
	var meta *http.Client
	if f.d.Credentials != nil {
		meta = f.d.Credentials(rel, full)
	}
	containerHost := ""
	if u, err := url.Parse(full); full != "" && err == nil {
		containerHost = u.Hostname()
	}
	return bedrock.NewChain(bedrock.ChainOptions{
		Region: region, Profile: profile, Metadata: meta, ContainerHost: containerHost, IMDSRegion: imdsRegion,
		System: func(hosts []string) *http.Client {
			c := f.d.Egress.SystemClient(core.SystemDest{Name: name + ":aws-credentials", Hosts: hosts})
			if c == nil {
				return nil
			}
			return harden(c)
		},
	})
}

// httpClientHosts is httpClient for a destination with several base URLs
// (Bedrock's runtime and control-plane endpoints).
func (f *Facade) httpClientHosts(name string, bases []string, allowPrivate bool) (*http.Client, error) {
	var hosts []string
	for _, b := range bases {
		hp, err := hostPort(b)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, hp)
	}
	c := f.d.Egress.SystemClient(core.SystemDest{Name: name, Hosts: hosts, AllowPrivate: allowPrivate})
	if c == nil {
		return nil, errors.New("egress returned no client")
	}
	return harden(c), nil
}

// bedrockEndpoints returns the runtime endpoint (base_url, a VPC endpoint,
// or the region's) and the control-plane endpoint.
func bedrockEndpoints(baseURL, region string) (runtime, control string, err error) {
	if !bedrock.ValidRegion(region) {
		return "", "", fmt.Errorf("bedrock region %q is not an AWS region name: %w", region, core.ErrInvalid)
	}
	runtime = strings.TrimRight(baseURL, "/")
	if runtime == "" {
		runtime = bedrock.RuntimeURL(region)
	} else if !strings.HasPrefix(runtime, "https://") {
		return "", "", fmt.Errorf("bedrock base_url must be https: %w", core.ErrInvalid)
	}
	return runtime, bedrock.ControlURL(region), nil
}

func (f *Facade) buildBedrock(p config.Provider, fp string) (*entry, error) {
	runtime, control, err := bedrockEndpoints(p.BaseURL, p.Region)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.ID, err)
	}
	hc, err := f.httpClientHosts("provider:"+p.ID, []string{runtime, control}, p.AllowPrivate)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.ID, err)
	}
	chain := f.awsChain("provider:"+p.ID, p.Region, p.Profile, false)
	creds := &credentials{f: f, id: p.ID, onInvalidate: chain.Invalidate}
	o := wire.Options{ID: p.ID, Kind: core.KindBedrock, BaseURL: runtime, HTTP: hc, Redact: creds.redact, Discover: p.Discover}
	for _, m := range p.Models {
		o.Models = append(o.Models, core.ModelInfo{Ref: core.ModelRef{Provider: p.ID, Model: m.ID}, Source: "config"})
	}
	cl := bedrock.New(bedrock.Options{Options: o, Region: p.Region, ControlURL: control, Credentials: chain})
	return &entry{id: p.ID, kind: core.KindBedrock, base: runtime, cfg: p, fp: fp, hc: hc, creds: creds, cl: cl, chain: chain}, nil
}

// probeBedrock tests Bedrock access before the provider is saved: it
// resolves the AWS credentials (reporting their source, never their
// values), lists the usable Claude and Nova models for the picker, and runs
// the probe's completion and tool call on the chosen model, by default a
// small one.
func (f *Facade) probeBedrock(ctx context.Context, pc core.ProbeConfig) (core.ProbeResult, error) {
	if pc.Region != "" && !bedrock.ValidRegion(pc.Region) {
		return core.ProbeResult{}, fmt.Errorf("provider: region %q is not an AWS region name: %w", pc.Region, core.ErrInvalid)
	}
	if pc.Profile != "" && !config.ValidAWSProfile(pc.Profile) {
		return core.ProbeResult{}, fmt.Errorf("provider: invalid AWS profile name %q: %w", pc.Profile, core.ErrInvalid)
	}
	if pc.BaseURL != "" && !strings.HasPrefix(pc.BaseURL, "https://") {
		return core.ProbeResult{}, fmt.Errorf("provider: bedrock base_url must be https: %w", core.ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var res core.ProbeResult
	chain := f.awsChain("provider:probe", pc.Region, pc.Profile, true)
	src, err := chain.Describe(ctx)
	if err != nil {
		res.Detail = awsCredentialsText(err)
		return res, nil
	}
	res.Credentials = src
	region := pc.Region
	if region == "" {
		region = chain.Region(ctx)
	}
	if region == "" {
		res.Detail = "AWS access works (" + src + "), but no region is configured; choose the AWS region to use Bedrock in."
		return res, nil
	}
	runtime, control, err := bedrockEndpoints(pc.BaseURL, region)
	if err != nil {
		return res, err
	}
	hc, err := f.httpClientHosts("provider:probe", []string{runtime, control}, pc.AllowPrivate)
	if err != nil {
		return res, err
	}
	defer hc.CloseIdleConnections()
	creds := &credentials{f: f, id: "probe"}
	cl := bedrock.New(bedrock.Options{Options: wire.Options{ID: "probe", Kind: core.KindBedrock, BaseURL: runtime, HTTP: hc, Redact: creds.redact},
		Region: region, ControlURL: control, Credentials: chain})
	found, derr := cl.Discover(ctx)
	for _, m := range found {
		res.Models = append(res.Models, m.Ref.Model)
	}
	res.Choices = choices(found)
	model := pc.Model
	if model == "" {
		model = probeModel(res.Models)
	}
	if model == "" {
		if derr != nil {
			res.Detail = plain(derr, runtime, core.KindBedrock)
		} else {
			res.Detail = "This AWS account lists no Claude or Nova models in " + region + "; name a model id to test."
		}
		return res, nil
	}
	info := f.probeInfo(core.KindBedrock, runtime, model, found)
	notes, ok := f.exercise(ctx, cl, model, info, 0, bedrockFirstMax, runtime, core.KindBedrock, &res)
	if ok {
		res.Detail = strings.Join(notes, " ")
	}
	return res, nil
}

// probeModel picks a small, fast model that does not think by default for
// an unattended probe.
func probeModel(models []string) string {
	for _, want := range []string{"anthropic.claude-haiku-4-5", "amazon.nova-lite", "amazon.nova-micro", "amazon.nova-2-lite"} {
		if i := slices.IndexFunc(models, func(m string) bool { return strings.Contains(m, want) }); i >= 0 {
			return models[i]
		}
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

// awsCredentialsText explains a credential chain failure in one or two
// sentences with what to do next.
func awsCredentialsText(err error) string {
	msg := bedrock.ScrubAccounts(err.Error())
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	hint := "On a laptop, sign in with the AWS CLI (aws sso login) and, in a container, mount ~/.aws read-only and set AWS_PROFILE; on a cloud host, attach a role to the instance, task or service account."
	if strings.Contains(msg, "credential_process") {
		hint = "Use an SSO profile, a role or access keys instead."
	}
	return "Bothy could not get AWS credentials: " + msg + ". " + hint
}

func bedrockAuthText(msg string) string {
	switch {
	case strings.HasPrefix(msg, "AWS credentials unavailable: "):
		return awsCredentialsText(errors.New(strings.TrimPrefix(msg, "AWS credentials unavailable: ")))
	case strings.Contains(msg, "AccessDeniedException"):
		return "AWS denied this identity access to the model; allow bedrock:InvokeModelWithResponseStream for it, and request model access in the Bedrock console if needed: " + msg
	default:
		return "AWS rejected the request's credentials: " + msg
	}
}

// awsSignals reports whether the environment or the shared files suggest
// AWS credentials, so Detect may spend longer resolving them.
func awsSignals(env map[string]string) bool {
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI"} {
		if strings.TrimSpace(env[k]) != "" {
			return true
		}
	}
	files := []string{env["AWS_CONFIG_FILE"], env["AWS_SHARED_CREDENTIALS_FILE"]}
	if home := env["HOME"]; home != "" {
		files = append(files, filepath.Join(home, ".aws", "config"), filepath.Join(home, ".aws", "credentials"))
	}
	for _, p := range files {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// detectAWS offers Bedrock when this server's AWS credentials resolve
// ("Use this server's AWS access"), with the region and models when known.
func (f *Facade) detectAWS(ctx context.Context) *core.Detection {
	env := envMap(f.d.Env())
	timeout := detectTimeout
	if awsSignals(env) {
		timeout = awsDetectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	chain := f.awsChain("provider:detect", "", "", true)
	src, err := chain.Describe(ctx)
	if err != nil {
		return nil
	}
	d := &core.Detection{Kind: core.KindBedrock, Source: src, Region: chain.Region(ctx)}
	if env["AWS_PROFILE"] != "" {
		d.Profile = chain.Profile(ctx)
	}
	d.Note = "Use this server's AWS access (" + src + ")"
	if d.Region == "" {
		d.Note += "; choose a region."
		return d
	}
	d.BaseURL = bedrock.RuntimeURL(d.Region)
	d.Note += " in " + d.Region + "."
	hc, err := f.httpClientHosts("provider:detect", []string{d.BaseURL, bedrock.ControlURL(d.Region)}, false)
	if err != nil {
		return d
	}
	defer hc.CloseIdleConnections()
	cl := bedrock.New(bedrock.Options{Options: wire.Options{ID: "detect", Kind: core.KindBedrock, HTTP: hc},
		Region: d.Region, Credentials: chain})
	if ms, err := cl.Discover(ctx); err == nil {
		for _, m := range ms {
			d.Models = append(d.Models, m.Ref.Model)
		}
	}
	return d
}

// bedrockCredentialsCheck is the doctor's providers.bedrock_credentials: for
// each enabled bedrock provider, where its AWS credentials come from
// (info), or why they cannot be resolved (high).
type bedrockCredentialsCheck struct{ f *Facade }

func (bedrockCredentialsCheck) ID() string { return "providers.bedrock_credentials" }

func (c bedrockCredentialsCheck) Run(ctx context.Context) []core.Finding {
	cfg := c.f.d.Config.Current()
	if cfg == nil {
		return nil
	}
	var out []core.Finding
	for _, p := range cfg.Providers {
		if !enabled(p) || p.Type != string(core.KindBedrock) {
			continue
		}
		f := core.Finding{ID: "providers.bedrock_credentials", Area: "providers", Fix: "/settings/providers"}
		e, err := c.f.entry(p.ID)
		if err != nil {
			f.Title, f.Detail, f.Severity = "Provider "+p.ID+" cannot be set up", err.Error(), core.SevHigh
			out = append(out, f)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, awsCheckTimeout)
		src, err := e.chain.Describe(cctx)
		cancel()
		if err != nil {
			f.Title, f.Detail, f.Severity = "Provider "+p.ID+" cannot get AWS credentials", awsCredentialsText(err), core.SevHigh
			if p.Profile != "" {
				f.Fix = "aws sso login --profile " + p.Profile
			}
			out = append(out, f)
			continue
		}
		f.Title = "Provider " + p.ID + " signs in with " + src
		f.Detail = "Bedrock in " + p.Region + ". Bothy never stores or shows these credentials; they are resolved when needed and kept in memory only."
		f.Severity = core.SevInfo
		out = append(out, f)
	}
	return out
}

func (bedrockCredentialsCheck) Fix(context.Context, core.Finding) error {
	return core.ErrNotImplemented
}
