package vault

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/vault/awssm"
)

const (
	awsSource = "aws-sm"
	// awsDest names the egress system destination of Secrets Manager calls.
	awsDest            = "vault:aws-sm"
	awsFetchTimeout    = 20 * time.Second
	awsCheckTimeout    = 15 * time.Second
	defaultAWSCacheTTL = 5 * time.Minute
	// awsKeptVersions bounds how many versions of one secret stay registered
	// with the redactor: the current one and the one it replaced, which may
	// still sit in output produced just before the rotation.
	awsKeptVersions = 2
)

// AWS connects the vault to AWS Secrets Manager for aws-sm references.
type AWS struct {
	// Credentials returns the AWS credential chain for a region and a
	// shared-config profile ("" = AWS_PROFILE or the default chain).
	// cmd/bothy passes the bedrock provider's chain, so both use the same
	// egress rules and refuse credential_process profiles alike.
	Credentials func(region, profile string) awssm.Credentials
	// HTTP returns the egress system client for d.
	HTTP func(d core.SystemDest) *http.Client
	// Refs lists the references kept outside the configuration (GitHub
	// connections), for List, Status and the doctor; nil = none.
	Refs func(ctx context.Context) []string
}

// SetAWS enables aws-sm references. The vault is built before egress and
// the providers, so cmd/bothy calls this once they exist; until then (and
// in offline CLI commands) such references fail with a plain error.
func (v *Vault) SetAWS(a AWS) {
	s := &v.aws
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps = &a
	s.chains = map[string]awssm.Credentials{}
}

// ExternalError is a failure to read a secret from a cloud secret store.
// Its message names the reference and what to do next, never a value.
type ExternalError struct {
	Ref  string
	Code core.FailureCode
	Msg  string
	errs []error
}

func (e *ExternalError) Error() string                 { return "vault: " + e.Msg }
func (e *ExternalError) Unwrap() []error               { return e.errs }
func (e *ExternalError) FailureCode() core.FailureCode { return e.Code }

type awsState struct {
	mu       sync.Mutex
	deps     *AWS
	chains   map[string]awssm.Credentials // by region|profile
	cache    map[string]*awsEntry         // by region|profile|secret id; dropped once expired
	keys     map[string]*awsKey           // by the same key; outlives the cached value
	inflight map[string]*awsCall
	used     map[string]time.Time // by canonical reference
}

type awsEntry struct {
	value   []byte // the whole SecretString or SecretBinary
	version string
	fetched time.Time
}

// awsKey is what the vault remembers about a secret once its value has
// expired: when it was last fetched and what the redactor holds of it.
type awsKey struct {
	fetched  time.Time
	versions []awsVersion // oldest first
}

type awsVersion struct {
	id     string   // VersionId
	redact []string // redactor ids of the value and of the JSON keys read from it
}

type awsCall struct {
	done chan struct{}
	ent  *awsEntry
	err  error
}

func (s *awsState) init() {
	s.cache, s.keys, s.inflight, s.used = map[string]*awsEntry{}, map[string]*awsKey{}, map[string]*awsCall{}, map[string]time.Time{}
}

func (v *Vault) current() *config.Config {
	if v.cfg == nil {
		return config.Default()
	}
	if c := v.cfg.Current(); c != nil {
		return c
	}
	return config.Default()
}

// awsWhere is the region, profile and cache TTL a reference is read with.
func (v *Vault) awsWhere(r config.AWSSecretRef) (region, profile string, ttl time.Duration) {
	a := v.current().Secrets.AWS
	ttl = a.CacheTTL.D()
	if ttl <= 0 {
		ttl = defaultAWSCacheTTL
	}
	return r.RegionOr(a.Region), cmp.Or(r.Profile, a.Profile), ttl
}

func awsCacheKey(region, profile, id string) string { return region + "|" + profile + "|" + id }

func (v *Vault) awsChain(region, profile string) (awssm.Credentials, *AWS) {
	s := &v.aws
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deps == nil || s.deps.Credentials == nil {
		return nil, nil
	}
	k := region + "|" + profile
	c := s.chains[k]
	if c == nil {
		c = s.deps.Credentials(region, profile)
		s.chains[k] = c
	}
	return c, s.deps
}

// getAWS resolves one aws-sm reference. Every use, a cached one included,
// is audited as secret.use before the value is released; a failure is
// audited too, with its code.
func (v *Vault) getAWS(ctx context.Context, r config.AWSSecretRef, purpose string) (core.Secret, error) {
	ref := r.String()
	if v.audit == nil {
		return core.Secret{}, errors.New("vault: no auditor to record secret.use")
	}
	region, profile, ttl := v.awsWhere(r)
	detail := map[string]any{"ref": ref, "purpose": purpose, "source": awsSource, "scope": string(core.GlobalScope), "region": region}
	fail := func(e *ExternalError) (core.Secret, error) {
		detail["code"] = string(e.Code)
		if err := v.record(ctx, "secret.use", "system", ref, "error", detail); err != nil {
			v.log.Error("vault: auditing a failed secret.use", "ref", awssm.ScrubAccounts(ref), "err", err)
		}
		return core.Secret{}, e
	}
	if region == "" {
		return fail(&ExternalError{Ref: ref, Code: core.FailUnknown, errs: []error{core.ErrInvalid},
			Msg: "no AWS region for " + awssm.ScrubAccounts(ref) + ": set secrets.aws.region (for example us-west-2) or add ?region=<region> to the reference"})
	}
	// The region becomes the endpoint's host name; config validation drops
	// a malformed secrets.aws.region, but nothing here relies on it.
	if !config.ValidAWSSecretsRegion(region) {
		return fail(&ExternalError{Ref: ref, Code: core.FailUnknown, errs: []error{core.ErrInvalid},
			Msg: "secrets.aws.region is not an AWS region name (for example us-west-2)"})
	}
	key := awsCacheKey(region, profile, r.SecretID)
	ent, cached, err := v.awsLoad(ctx, key, ttl, func(fctx context.Context) (*awssm.Value, error) {
		return v.fetchAWS(fctx, region, profile, r.SecretID)
	})
	if err != nil {
		if ctx.Err() != nil {
			return core.Secret{}, context.Cause(ctx)
		}
		return fail(awsFailure(r, region, err))
	}
	val, xerr := awsExtract(ent.value, r)
	if xerr != nil {
		return fail(xerr)
	}
	if r.Key != "" {
		v.awsRegister(key, ent.version, "#"+r.Key, val)
	}
	detail["version"], detail["cached"] = ent.version, cached
	if err := v.record(ctx, "secret.use", "system", ref, "ok", detail); err != nil {
		return core.Secret{}, err
	}
	v.aws.mu.Lock()
	v.aws.used[ref] = v.clock.Now()
	v.aws.mu.Unlock()
	return core.NewSecret(val), nil
}

// awsLoad returns the cached entry for key while it is younger than ttl,
// else fetches it once for all concurrent callers. The fetch runs detached
// from ctx, so a caller that gives up does not fail the others. Expired
// values of every secret leave the cache here.
func (v *Vault) awsLoad(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) (*awssm.Value, error)) (*awsEntry, bool, error) {
	s := &v.aws
	s.mu.Lock()
	now := v.clock.Now()
	for k, e := range s.cache {
		if now.Sub(e.fetched) >= ttl {
			delete(s.cache, k)
		}
	}
	if e := s.cache[key]; e != nil {
		s.mu.Unlock()
		return e, true, nil
	}
	c := s.inflight[key]
	if c == nil {
		c = &awsCall{done: make(chan struct{})}
		s.inflight[key] = c
		go v.awsFetchInto(context.WithoutCancel(ctx), key, c, fetch)
	}
	s.mu.Unlock()
	select {
	case <-c.done:
		return c.ent, false, c.err
	case <-ctx.Done():
		return nil, false, context.Cause(ctx)
	}
}

func (v *Vault) awsFetchInto(ctx context.Context, key string, c *awsCall, fetch func(context.Context) (*awssm.Value, error)) {
	ctx, cancel := context.WithTimeout(ctx, awsFetchTimeout)
	defer cancel()
	val, err := fetch(ctx)
	s := &v.aws
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(c.done)
	delete(s.inflight, key)
	if err != nil {
		c.err = err
		return
	}
	raw := val.String
	if raw == nil {
		raw = val.Binary
	}
	ent := &awsEntry{value: raw, version: val.VersionID, fetched: v.clock.Now()}
	k := s.keys[key]
	if k == nil {
		k = &awsKey{}
		s.keys[key] = k
	}
	k.fetched = ent.fetched
	if n := len(k.versions); n == 0 || k.versions[n-1].id != ent.version {
		k.versions = append(k.versions, awsVersion{id: ent.version})
	}
	for len(k.versions) > awsKeptVersions {
		for _, id := range k.versions[0].redact {
			v.red.Remove(id)
		}
		k.versions = k.versions[1:]
	}
	s.cache[key] = ent
	c.ent = ent
	v.registerLocked(key, ent.version, "", raw)
}

// awsRegister registers a value read from a JSON secret with the redactor,
// once per version.
func (v *Vault) awsRegister(key, version, suffix string, val []byte) {
	v.aws.mu.Lock()
	defer v.aws.mu.Unlock()
	v.registerLocked(key, version, suffix, val)
}

func (v *Vault) registerLocked(key, version, suffix string, val []byte) {
	id := "aws-sm:" + key + suffix + "@" + version
	if k := v.aws.keys[key]; k != nil {
		for i := range k.versions {
			if k.versions[i].id != version {
				continue
			}
			if slices.Contains(k.versions[i].redact, id) {
				return
			}
			k.versions[i].redact = append(k.versions[i].redact, id)
		}
	}
	v.red.Add(id, val)
}

func (v *Vault) fetchAWS(ctx context.Context, region, profile, id string) (*awssm.Value, error) {
	creds, deps := v.awsChain(region, profile)
	if deps == nil || deps.HTTP == nil {
		return nil, errAWSUnavailable
	}
	hc := deps.HTTP(core.SystemDest{Name: awsDest, Hosts: []string{awssm.Host(region)}, AllowPrivate: v.current().Secrets.AWS.AllowPrivate})
	cl, err := awssm.New(awssm.Options{Region: region, Credentials: creds, HTTP: hc, Now: v.clock.Now})
	if err != nil {
		return nil, err
	}
	val, err := cl.GetSecretValue(ctx, id)
	var ae *awssm.Error
	if errors.As(err, &ae) && ae.Code == core.FailAWSNoCredentials && ae.AWSCode != "" {
		// AWS rejected the credentials (expired session, rotated role): the
		// next use resolves fresh ones.
		if inv, ok := creds.(interface{ Invalidate() }); ok {
			inv.Invalidate()
		}
	}
	return val, err
}

var errAWSUnavailable = errors.New("AWS Secrets Manager is not available in this process (offline command)")

// awsExtract returns the whole value, or the JSON key the reference names.
// A string is returned as is; any other JSON value as its compact JSON text.
func awsExtract(raw []byte, r config.AWSSecretRef) ([]byte, *ExternalError) {
	if r.Key == "" {
		return bytes.Clone(raw), nil
	}
	shown := awssm.ScrubAccounts(r.SecretID)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, &ExternalError{Ref: r.String(), Code: core.FailNotJSON, errs: []error{core.ErrInvalid},
			Msg: "the secret " + shown + " is not a JSON object, so #" + r.Key + " cannot be read from it; drop #" + r.Key + " to use the whole value"}
	}
	field, ok := obj[r.Key]
	if !ok || string(field) == "null" {
		return nil, &ExternalError{Ref: r.String(), Code: core.FailJSONKeyMissing, errs: []error{core.ErrNotFound},
			Msg: "the secret " + shown + " has no JSON key \"" + r.Key + "\""}
	}
	var s string
	if json.Unmarshal(field, &s) == nil {
		return []byte(s), nil
	}
	var buf bytes.Buffer
	if json.Compact(&buf, field) == nil {
		return buf.Bytes(), nil
	}
	return bytes.Clone(field), nil
}

// awsFailure explains a failed fetch in plain words with what to do next.
func awsFailure(r config.AWSSecretRef, region string, err error) *ExternalError {
	ref := r.String()
	shown, id := awssm.ScrubAccounts(ref), awssm.ScrubAccounts(r.SecretID)
	cause := awssm.ScrubAccounts(err.Error())
	if len(cause) > 400 {
		cause = cause[:400] + "…"
	}
	e := &ExternalError{Ref: ref, Code: core.CodeOf(err), errs: []error{err}}
	if errors.Is(err, errAWSUnavailable) {
		e.Code = core.FailUnknown
	}
	switch e.Code {
	case core.FailAWSNoCredentials:
		hint := "On a laptop, sign in with aws sso login and set AWS_PROFILE (or secrets.aws.profile); in a container, also mount ~/.aws; " +
			"on AWS, give the instance, task or pod a role."
		// The SDK renews an expired SSO access token in place, so a read-only
		// ~/.aws fails here hours before the SSO session itself ends.
		if strings.Contains(cause, "unable to cache refreshed SSO token") || strings.Contains(cause, "save token:") {
			hint = "The AWS sign-in token needed renewing and Bothy cannot save it to ~/.aws, which is read-only here: run aws sso login on the host again, " +
				"or mount ~/.aws/sso/cache writable as compose.yaml shows."
		}
		e.Msg = "Bothy could not get AWS credentials to read " + shown + ": " + cause + ". " + hint
	case core.FailAWSDenied:
		e.Msg = "AWS refused this server's identity access to " + shown + " in " + region + ": allow secretsmanager:GetSecretValue on the secret, " +
			"and kms:Decrypt on its key if it uses a customer managed key (" + cause + ")."
	case core.FailNotFound:
		e.errs = append(e.errs, core.ErrNotFound)
		e.Msg = "AWS Secrets Manager in " + region + " has no secret " + id + " that this server can read (or it is scheduled for deletion); check the name and the region."
	case core.FailRateLimited:
		e.Msg = "AWS Secrets Manager is limiting requests, so " + shown + " could not be read now; Bothy tries again on its next use."
	case core.FailPrivateAddress:
		e.Msg = "the Secrets Manager endpoint for " + region + " resolves to a private address, which Bothy refuses unless secrets.aws.allow_private is true " +
			"(an interface VPC endpoint with private DNS): " + cause + "."
	case core.FailTimeout, core.FailUnreachable, core.FailTLS:
		e.Msg = "Bothy could not reach AWS Secrets Manager in " + region + " to read " + shown + ": " + cause + "."
	default:
		if e.Code == "" {
			e.Code = core.FailUnknown
		}
		e.Msg = "AWS Secrets Manager could not return " + shown + ": " + cause + "."
	}
	return e
}

// awsRefUse is an aws-sm reference the server makes.
type awsRefUse struct {
	ref    config.AWSSecretRef
	where  string // config path, or "a GitHub connection"
	region string
}

// awsRefs lists the distinct aws-sm references of the configuration and of
// the stores outside it, in a stable order.
func (v *Vault) awsRefs(ctx context.Context) []awsRefUse {
	seen := map[string]bool{}
	var out []awsRefUse
	add := func(where, val string) {
		refs, err := config.FindAWSSecretRefs(val)
		if err != nil {
			return
		}
		for _, r := range refs {
			if k := r.Ref.String(); !seen[k] {
				seen[k] = true
				region, _, _ := v.awsWhere(r.Ref)
				out = append(out, awsRefUse{ref: r.Ref, where: where, region: region})
			}
		}
	}
	for _, sf := range v.current().SecretFields() {
		add(sf.Path, sf.Value)
	}
	v.aws.mu.Lock()
	var extra func(context.Context) []string
	if v.aws.deps != nil {
		extra = v.aws.deps.Refs
	}
	v.aws.mu.Unlock()
	if extra != nil {
		for _, s := range extra(ctx) {
			add("a GitHub connection", s)
		}
	}
	return out
}

// awsMetas lists the references for List: no hint, the time the value was
// last fetched and last used.
func (v *Vault) awsMetas(ctx context.Context) []core.SecretMeta {
	refs := v.awsRefs(ctx)
	out := make([]core.SecretMeta, 0, len(refs))
	v.aws.mu.Lock()
	defer v.aws.mu.Unlock()
	for _, u := range refs {
		m := core.SecretMeta{Scope: core.GlobalScope, Name: u.ref.String(), Source: awsSource, LastUsed: v.aws.used[u.ref.String()]}
		_, profile, _ := v.awsWhere(u.ref)
		if k := v.aws.keys[awsCacheKey(u.region, profile, u.ref.SecretID)]; k != nil {
			m.UpdatedAt = k.fetched
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b core.SecretMeta) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (v *Vault) checkAWSAccess(ctx context.Context) []core.Finding {
	refs := v.awsRefs(ctx)
	if len(refs) == 0 {
		return nil
	}
	type group struct{ region, profile string }
	groups := map[group][]string{}
	for _, u := range refs {
		_, profile, _ := v.awsWhere(u.ref)
		g := group{u.region, profile}
		groups[g] = append(groups[g], u.where)
	}
	ttl := v.current().Secrets.AWS.CacheTTL.D()
	var out []core.Finding
	for _, g := range slices.SortedFunc(maps.Keys(groups), func(a, b group) int {
		return cmp.Or(strings.Compare(a.region, b.region), strings.Compare(a.profile, b.profile))
	}) {
		where := groups[g]
		n := fmt.Sprintf("%d reference", len(where))
		if len(where) != 1 {
			n += "s"
		}
		f := core.Finding{ID: "secrets.aws_access", Area: "vault", Fix: "/settings/vault"}
		if g.region == "" {
			f.Severity, f.Title = core.SevHigh, "AWS secret references have no region"
			f.Detail = n + " to AWS Secrets Manager (" + strings.Join(where, ", ") + ") name no region. Set secrets.aws.region or add ?region=<region> to each."
			out = append(out, f)
			continue
		}
		creds, deps := v.awsChain(g.region, g.profile)
		if deps == nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, awsCheckTimeout)
		src, err := creds.Describe(cctx)
		cancel()
		if err != nil {
			f.Severity, f.Title = core.SevHigh, "Bothy cannot get AWS credentials for its secret references"
			f.Detail = n + " to AWS Secrets Manager in " + g.region + " (" + strings.Join(where, ", ") + ") cannot be read: " +
				awssm.ScrubAccounts(err.Error()) + ". On a laptop, sign in with aws sso login; on AWS, give the instance, task or pod a role."
			if g.profile != "" {
				f.Fix = "aws sso login --profile " + g.profile
			}
			out = append(out, f)
			continue
		}
		f.Severity, f.Fix = core.SevInfo, ""
		f.Title = "AWS secret references sign in with " + src
		f.Detail = n + " to AWS Secrets Manager in " + g.region + ". This check resolves the credentials only, never a value; values are fetched when used, " +
			"kept in memory for " + ttl.String() + " and never stored."
		out = append(out, f)
	}
	return out
}
