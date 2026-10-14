package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/vault/awssm"
)

const (
	dbJSON   = `{"username":"app","password":"pw-0123456789-db","port":5432,"opts":{"ssl":true}}`
	llmKey   = "sk-ant-api03-aws-0123456789abcdef"
	smARN    = "arn:aws:secretsmanager:eu-central-1:123456789012:secret:prod/eu-AbCdEf"
	euSecret = "eu-secret-value-0123456789"
)

type smSecret struct {
	version string
	str     *string
	bin     []byte
}

// fakeSM is AWS Secrets Manager's GetSecretValue over HTTP.
type fakeSM struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	secrets map[string]smSecret // by region|id
	fail    map[string][2]string
	reqs    []string // region|id per request
	gate    chan struct{}
}

func newFakeSM(t *testing.T) *fakeSM {
	f := &fakeSM{t: t, secrets: map[string]smSecret{}, fail: map[string][2]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSM) put(region, id, version, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[region+"|"+id] = smSecret{version: version, str: &value}
}

func (f *fakeSM) putBinary(region, id, version string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[region+"|"+id] = smSecret{version: version, bin: b}
}

// failWith makes requests for id answer with an AWS error type and message.
func (f *fakeSM) failWith(id, awsType, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[id] = [2]string{awsType, msg}
}

func (f *fakeSM) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakeSM) serve(w http.ResponseWriter, r *http.Request) {
	region := strings.TrimSuffix(strings.TrimPrefix(r.Host, "secretsmanager."), ".amazonaws.com")
	var in struct {
		SecretID string `json:"SecretId"`
	}
	b, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodPost || r.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" || json.Unmarshal(b, &in) != nil ||
		!strings.Contains(r.Header.Get("Authorization"), "/"+region+"/secretsmanager/aws4_request") {
		f.t.Errorf("unexpected request %s %s %q %q", r.Method, r.Host, r.Header.Get("X-Amz-Target"), b)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, region+"|"+in.SecretID)
	gate := f.gate
	s, ok := f.secrets[region+"|"+in.SecretID]
	fl, failing := f.fail[in.SecretID]
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	switch {
	case failing:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"__type": fl[0], "Message": fl[1]})
	case !ok:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"__type":"ResourceNotFoundException","Message":"Secrets Manager can't find the specified secret."}`)
	default:
		out := map[string]any{"Name": in.SecretID, "VersionId": s.version, "ARN": "arn:aws:secretsmanager:" + region + ":123456789012:secret:" + in.SecretID}
		if s.str != nil {
			out["SecretString"] = *s.str
		} else {
			out["SecretBinary"] = base64.StdEncoding.EncodeToString(s.bin)
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

// fakeChain stands in for the bedrock provider's credential chain.
type fakeChain struct {
	region, profile string
	fail            error
	invalidated     atomic.Int32
	retrieved       atomic.Int32
}

func (c *fakeChain) Retrieve(context.Context) (aws.Credentials, error) {
	c.retrieved.Add(1)
	if c.fail != nil {
		return aws.Credentials{}, c.fail
	}
	return aws.Credentials{AccessKeyID: "AKIDVAULTTEST", SecretAccessKey: "test-secret-access-key", SessionToken: "tok"}, nil
}

func (c *fakeChain) Describe(context.Context) (string, error) {
	if c.fail != nil {
		return "", c.fail
	}
	return "SSO profile " + c.profile, nil
}

func (c *fakeChain) Invalidate() { c.invalidated.Add(1) }

// awsHarness wires a vault to fakeSM through an egress stand-in that
// admits exactly the hosts of the SystemDest it was asked for.
type awsHarness struct {
	sm *fakeSM

	mu     sync.Mutex
	dests  []core.SystemDest
	chains map[string]*fakeChain
	fail   error // credentials failure for new chains
	extra  []string
}

type allowHosts struct {
	hosts []string
	srv   *httptest.Server
}

func (a allowHosts) RoundTrip(req *http.Request) (*http.Response, error) {
	if !slices.Contains(a.hosts, req.URL.Host+":443") {
		return nil, errors.New("egress: destination not allowed: " + req.URL.Host + " is not a configured host")
	}
	u, _ := url.Parse(a.srv.URL)
	r := req.Clone(req.Context())
	r.Host = req.URL.Host
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

func (h *awsHarness) deps() AWS {
	return AWS{
		Credentials: func(region, profile string) awssm.Credentials {
			h.mu.Lock()
			defer h.mu.Unlock()
			c := &fakeChain{region: region, profile: profile, fail: h.fail}
			h.chains[region+"|"+profile] = c
			return c
		},
		HTTP: func(d core.SystemDest) *http.Client {
			h.mu.Lock()
			h.dests = append(h.dests, d)
			h.mu.Unlock()
			return &http.Client{Transport: allowHosts{hosts: d.Hosts, srv: h.sm.srv}}
		},
		Refs: func(context.Context) []string { return h.extra },
	}
}

func (h *awsHarness) chain(region, profile string) *fakeChain {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.chains[region+"|"+profile]
}

func newAWSFixture(t *testing.T) (*fixture, *awsHarness) {
	f := newFixture(t)
	f.cfg.Secrets.AWS.Region = "us-west-2"
	f.cfg.Secrets.AWS.Profile = "demo"
	h := &awsHarness{sm: newFakeSM(t), chains: map[string]*fakeChain{}}
	h.sm.put("us-west-2", "prod/db", "v1", dbJSON)
	h.sm.put("us-west-2", "prod/llm", "v1", llmKey)
	h.sm.put("eu-central-1", smARN, "v1", euSecret)
	return f, h
}

func (f *fixture) openAWS(h *awsHarness) *Vault {
	v := f.open()
	v.SetAWS(h.deps())
	return v
}

func resolve(t *testing.T, v *Vault, ref string, u core.UserID) string {
	t.Helper()
	s, err := v.Resolve(context.Background(), ref, u, "provider:test")
	if err != nil {
		t.Fatalf("Resolve(%q): %v", ref, err)
	}
	return s.Reveal()
}

func TestAWSRefsResolve(t *testing.T) {
	f, h := newAWSFixture(t)
	v := f.openAWS(h)
	mustPut(t, v, core.GlobalScope, "org", "org-value-0123")
	h.sm.putBinary("us-west-2", "bin", "v1", []byte{0xff, 0, 'b', 'i', 'n', 'a', 'r', 'y'})
	for _, c := range []struct{ ref, want string }{
		{"aws-sm:prod/llm", llmKey},
		{"aws-sm:prod/db#password", "pw-0123456789-db"},
		{"aws-sm:prod/db#port", "5432"},
		{"aws-sm:prod/db#opts", `{"ssl":true}`},
		{"aws-sm:prod/db", dbJSON},
		{"aws-sm:bin", "\xff\x00binary"},
		{"Bearer aws-sm:prod/llm", "Bearer " + llmKey},
		{"u=aws-sm:prod/db#username;p=aws-sm:prod/db#password;o=secret:org", "u=app;p=pw-0123456789-db;o=org-value-0123"},
		// The ARN's ":secret:" is part of the reference, not a vault name.
		{"aws-sm:" + smARN, euSecret},
	} {
		if got := resolve(t, v, c.ref, ""); got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
	if got := h.sm.requests(); !slices.Equal(got, []string{"us-west-2|prod/llm", "us-west-2|prod/db", "us-west-2|bin", "eu-central-1|" + smARN}) {
		t.Errorf("one fetch per secret, from its own region: %v", got)
	}
}

func TestAWSRefsErrors(t *testing.T) {
	f, h := newAWSFixture(t)
	v := f.openAWS(h)
	h.sm.failWith("denied", "AccessDeniedException", "User: arn:aws:sts::123456789012:assumed-role/Bothy/jane@example.com is not authorized to perform: secretsmanager:GetSecretValue")
	h.sm.failWith("kms", "DecryptionFailure", "Access to KMS is not allowed")
	h.sm.failWith("expired", "ExpiredTokenException", "The security token included in the request is expired")
	ctx := context.Background()
	for _, c := range []struct {
		ref    string
		code   core.FailureCode
		is     error
		phrase string
	}{
		{"aws-sm:prod/db#pasword", core.FailJSONKeyMissing, core.ErrNotFound, `has no JSON key "pasword"`},
		{"aws-sm:prod/llm#key", core.FailNotJSON, core.ErrInvalid, "is not a JSON object"},
		{"aws-sm:missing", core.FailNotFound, core.ErrNotFound, "us-west-2 has no secret missing"},
		{"aws-sm:denied", core.FailAWSDenied, nil, "allow secretsmanager:GetSecretValue"},
		{"aws-sm:kms", core.FailAWSDenied, nil, "kms:Decrypt"},
		{"aws-sm:expired", core.FailAWSNoCredentials, nil, "aws sso login"},
	} {
		_, err := v.Resolve(ctx, c.ref, "", "provider:test")
		if core.CodeOf(err) != c.code || (c.is != nil && !errors.Is(err, c.is)) || !strings.Contains(err.Error(), c.phrase) {
			t.Errorf("Resolve(%q) = %v (code %s), want code %s with %q", c.ref, err, core.CodeOf(err), c.code, c.phrase)
		}
		var xe *ExternalError
		if !errors.As(err, &xe) || xe.Ref != c.ref {
			t.Errorf("%q: not an ExternalError for the ref: %#v", c.ref, err)
		}
		for _, leak := range []string{"pw-0123456789", llmKey, "123456789012", "jane@example.com"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%q: error carries %q: %v", c.ref, leak, err)
			}
		}
	}
	if n := h.chain("us-west-2", "demo").invalidated.Load(); n != 1 {
		t.Errorf("credentials AWS rejected are dropped: invalidated %d times", n)
	}

	h.fail = errors.New("the SSO session associated with this profile has expired")
	f2, _ := newAWSFixture(t)
	v2 := f2.openAWS(h)
	_, err := v2.Resolve(ctx, "aws-sm:prod/llm", "", "provider:test")
	if core.CodeOf(err) != core.FailAWSNoCredentials || !strings.Contains(err.Error(), "could not get AWS credentials") {
		t.Errorf("no credentials: %v", err)
	}

	f3, h3 := newAWSFixture(t)
	f3.cfg.Secrets.AWS.Region = ""
	v3 := f3.openAWS(h3)
	_, err = v3.Resolve(ctx, "aws-sm:prod/llm", "", "provider:test")
	if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "set secrets.aws.region") {
		t.Errorf("no region: %v", err)
	}
	if got := resolve(t, v3, "aws-sm:prod/llm?region=us-west-2", ""); got != llmKey {
		t.Errorf("?region= works without secrets.aws.region: %q", got)
	}

	for _, ref := range []string{"aws-sm:", "aws-sm:prod/db#", "Bearer aws-sm:x?zone=a"} {
		if _, err := v.Resolve(ctx, ref, "", "provider:test"); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Resolve(%q) = %v, want ErrInvalid", ref, err)
		}
	}
}

// TestAWSRefsAfterSecretMarker: "secret:aws-sm:…" names the vault secret
// aws-sm followed by text, as before aws-sm references existed; reading it
// as two overlapping references panicked.
func TestAWSRefsAfterSecretMarker(t *testing.T) {
	f, h := newAWSFixture(t)
	v := f.openAWS(h)
	mustPut(t, v, core.GlobalScope, "aws-sm", "vault-value-0123")
	if got := resolve(t, v, "Key secret:aws-sm:prod/llm", ""); got != "Key vault-value-0123:prod/llm" {
		t.Errorf("Resolve = %q", got)
	}
	if n := len(h.sm.requests()); n != 0 {
		t.Errorf("AWS was asked: %v", h.sm.requests())
	}
}

// TestAWSRefsRegionShape: the region is checked where it becomes a host
// name, whatever the configuration layer let through.
func TestAWSRefsRegionShape(t *testing.T) {
	f, h := newAWSFixture(t)
	f.cfg.Secrets.AWS.Region = "evil.example.com/x"
	v := f.openAWS(h)
	if _, err := v.Resolve(context.Background(), "aws-sm:prod/llm", "", "x"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Resolve = %v, want ErrInvalid", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.dests) != 0 {
		t.Errorf("egress was asked for %+v", h.dests)
	}
}

func TestAWSRefsUnavailableOffline(t *testing.T) {
	f, _ := newAWSFixture(t)
	v := f.open()
	_, err := v.Resolve(context.Background(), "aws-sm:prod/llm", "", "cli")
	if err == nil || !strings.Contains(err.Error(), "not available in this process") {
		t.Fatalf("Resolve without SetAWS = %v", err)
	}
}

func TestAWSRefsCacheAndRotation(t *testing.T) {
	f, h := newAWSFixture(t)
	clock := fakes.NewClock(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	f.clock = clock
	f.cfg.Secrets.AWS.CacheTTL = config.Duration(time.Minute)
	v := f.openAWS(h)
	h.sm.put("us-west-2", "rot", "v1", `{"token":"token-version-one-0123"}`)
	for range 3 {
		if got := resolve(t, v, "aws-sm:rot#token", ""); got != "token-version-one-0123" {
			t.Fatalf("v1 = %q", got)
		}
	}
	clock.Advance(59 * time.Second)
	resolve(t, v, "aws-sm:rot#token", "")
	if n := len(h.sm.requests()); n != 1 {
		t.Fatalf("within the TTL: %d requests", n)
	}

	h.sm.put("us-west-2", "rot", "v2", `{"token":"token-version-two-4567"}`)
	clock.Advance(2 * time.Second)
	if got := resolve(t, v, "aws-sm:rot#token", ""); got != "token-version-two-4567" {
		t.Fatalf("after the TTL the new version is read: %q", got)
	}
	if n := len(h.sm.requests()); n != 2 {
		t.Fatalf("after the TTL: %d requests", n)
	}
	red := v.Redactor()
	if out := red.Redact("a=token-version-one-0123 b=token-version-two-4567"); strings.Contains(out, "version") {
		t.Errorf("current and previous versions stay masked: %q", out)
	}

	h.sm.put("us-west-2", "rot", "v3", `{"token":"token-version-three-89"}`)
	clock.Advance(time.Minute)
	resolve(t, v, "aws-sm:rot#token", "")
	if out := red.Redact("token-version-one-0123"); out != "token-version-one-0123" {
		t.Errorf("only two versions of a secret stay registered: %q", out)
	}
	if out := red.Redact("token-version-two-4567 token-version-three-89"); strings.Contains(out, "version") {
		t.Errorf("v2 and v3 masked: %q", out)
	}

	// A failed refresh is not cached: the next use asks again.
	h.sm.failWith("rot", "ThrottlingException", "Rate exceeded")
	clock.Advance(time.Minute)
	if _, err := v.Resolve(context.Background(), "aws-sm:rot#token", "", "x"); core.CodeOf(err) != core.FailRateLimited {
		t.Fatalf("throttled: %v", err)
	}
	if _, err := v.Resolve(context.Background(), "aws-sm:rot#token", "", "x"); core.CodeOf(err) != core.FailRateLimited {
		t.Fatalf("throttled again: %v", err)
	}
	if n := len(h.sm.requests()); n != 5 {
		t.Errorf("failures are retried on each use: %d requests", n)
	}
}

func TestAWSRefsExpiredValuesLeaveMemory(t *testing.T) {
	f, h := newAWSFixture(t)
	clock := fakes.NewClock(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	f.clock = clock
	v := f.openAWS(h)
	resolve(t, v, "aws-sm:prod/db#password", "")
	clock.Advance(defaultAWSCacheTTL)
	resolve(t, v, "aws-sm:prod/llm", "")
	v.aws.mu.Lock()
	_, kept := v.aws.cache[awsCacheKey("us-west-2", "demo", "prod/db")]
	v.aws.mu.Unlock()
	if kept {
		t.Error("an expired value stays cached")
	}
	if out := v.Redactor().Redact("pw-0123456789-db"); out == "pw-0123456789-db" {
		t.Error("output still masks a value whose cache entry expired")
	}
}

func TestAWSRefsConcurrentUsesFetchOnce(t *testing.T) {
	f, h := newAWSFixture(t)
	v := f.openAWS(h)
	h.sm.mu.Lock()
	h.sm.gate = make(chan struct{})
	h.sm.mu.Unlock()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			s, err := v.Resolve(context.Background(), "aws-sm:prod/db#password", "", "x")
			if err == nil && s.Reveal() != "pw-0123456789-db" {
				err = errors.New("wrong value")
			}
			errs <- err
		})
	}
	for len(h.sm.requests()) == 0 {
		time.Sleep(time.Millisecond)
	}
	// A caller that gives up does not fail the fetch the others wait for.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Resolve(ctx, "aws-sm:prod/db#password", "", "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled caller: %v", err)
	}
	close(h.sm.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := len(h.sm.requests()); n != 1 {
		t.Errorf("%d fetches for concurrent uses, want 1", n)
	}
}

func TestAWSRefsRedactionAndAudit(t *testing.T) {
	f, h := newAWSFixture(t)
	v := f.openAWS(h)
	resolve(t, v, "aws-sm:prod/db#password", "")
	resolve(t, v, "aws-sm:prod/db#password", "")
	resolve(t, v, "Bearer aws-sm:prod/llm", "")
	_, _ = v.Resolve(context.Background(), "aws-sm:prod/db#nope", "", "channel:telegram")

	out := v.Redactor().Redact("pw=pw-0123456789-db key=" + llmKey + " blob=" + dbJSON)
	for _, leak := range []string{"pw-0123456789-db", llmKey, `"username":"app"`} {
		if strings.Contains(out, leak) {
			t.Errorf("not masked: %q in %q", leak, out)
		}
	}

	var uses []core.AuditEntry
	for _, e := range f.entries() {
		for _, leak := range []string{"pw-0123456789", llmKey, "test-secret-access-key"} {
			if strings.Contains(string(e.Detail), leak) || strings.Contains(e.Target, leak) {
				t.Errorf("audit carries a value: %+v", e)
			}
		}
		if e.Action == "secret.use" {
			uses = append(uses, e)
		}
	}
	if len(uses) != 4 {
		t.Fatalf("every use is audited: %d secret.use rows", len(uses))
	}
	var d0, d1, d3 map[string]any
	_ = json.Unmarshal(uses[0].Detail, &d0)
	_ = json.Unmarshal(uses[1].Detail, &d1)
	_ = json.Unmarshal(uses[3].Detail, &d3)
	if uses[0].Target != "aws-sm:prod/db#password" || uses[0].Outcome != "ok" || d0["purpose"] != "provider:test" ||
		d0["source"] != "aws-sm" || d0["version"] != "v1" || d0["cached"] != false || d0["region"] != "us-west-2" {
		t.Errorf("first use %+v %v", uses[0], d0)
	}
	if d1["cached"] != true {
		t.Errorf("second use is served from the cache and still audited: %v", d1)
	}
	if uses[3].Outcome != "error" || d3["code"] != "json_key_missing" || d3["purpose"] != "channel:telegram" {
		t.Errorf("failed use %+v %v", uses[3], d3)
	}

	f.audit = &failingAuditor{}
	v2 := f.openAWS(h)
	if s, err := v2.Resolve(context.Background(), "aws-sm:prod/llm", "", "x"); err == nil {
		t.Errorf("Resolve returned %q although its audit failed", s.Reveal())
	}
}

// TestAWSRefsIDOR: aws-sm references come from the configuration only.
// A user cannot reach one through a vault value, a user scope never lists
// them, and they are company-wide whoever resolves them.
func TestAWSRefsIDOR(t *testing.T) {
	f, h := newAWSFixture(t)
	f.cfg.Providers = append(f.cfg.Providers, config.Provider{ID: "anthropic", Type: "anthropic", APIKey: "aws-sm:prod/llm"})
	f.cfg.MCP.Servers = append(f.cfg.MCP.Servers, config.MCPServer{Name: "db", Transport: "http", URL: "https://mcp.example.com",
		Headers: map[string]string{"Authorization": "Basic aws-sm:prod/db#password"}})
	h.extra = []string{"aws-sm:prod/github#token", "secret:github_pat"}
	ctx := context.Background()
	v := f.openAWS(h)
	u := f.user()
	us := core.UserScope(u)
	mustPut(t, v, us, "mine", "aws-sm:prod/db#password")
	if got := resolve(t, v, "secret:mine", u); got != "aws-sm:prod/db#password" {
		t.Errorf("a vault value is never resolved again: %q", got)
	}
	if n := len(h.sm.requests()); n != 0 {
		t.Fatalf("a user's vault value reached AWS: %v", h.sm.requests())
	}

	if got := resolve(t, v, "Basic aws-sm:prod/db#password", u); got != "Basic pw-0123456789-db" {
		t.Errorf("user context: %q", got)
	}
	if got := resolve(t, v, "Basic aws-sm:prod/db#password", ""); got != "Basic pw-0123456789-db" {
		t.Errorf("global context: %q", got)
	}
	if n := len(h.sm.requests()); n != 1 {
		t.Errorf("user and global uses share one company-wide value: %d fetches", n)
	}

	mine, err := v.List(ctx, us)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mine {
		if m.Source == "aws-sm" {
			t.Errorf("a user scope lists a company reference: %+v", m)
		}
	}
	all, err := v.List(ctx, core.GlobalScope)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range all {
		if m.Source == "aws-sm" {
			names = append(names, m.Name)
			if m.Hint != "" || m.Scope != core.GlobalScope {
				t.Errorf("aws-sm entry %+v", m)
			}
			if m.Name == "aws-sm:prod/db#password" && (m.UpdatedAt.IsZero() || m.LastUsed.IsZero()) {
				t.Errorf("fetched and used times: %+v", m)
			}
		}
	}
	if !slices.Equal(names, []string{"aws-sm:prod/db#password", "aws-sm:prod/github#token", "aws-sm:prod/llm"}) {
		t.Errorf("global list of aws-sm refs = %v", names)
	}
	if st := v.Status(); st.External["aws-sm"] != 3 || st.Secrets != 1 {
		t.Errorf("status %+v", st)
	}
	for _, sc := range []core.Scope{core.GlobalScope, us} {
		if err := v.Delete(ctx, sc, "aws-sm:prod/llm", u); !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "not stored in the vault") {
			t.Errorf("Delete(%s, aws ref) = %v", sc, err)
		}
	}
}

// TestAWSRefsEgress: Secrets Manager is reached only through the egress
// system client for the exact regional host.
func TestAWSRefsEgress(t *testing.T) {
	f, h := newAWSFixture(t)
	f.cfg.Secrets.AWS.AllowPrivate = true
	v := f.openAWS(h)
	h.sm.put("eu-west-1", "prod/llm", "v1", "eu-west-value-0123")
	resolve(t, v, "aws-sm:prod/llm", "")
	resolve(t, v, "aws-sm:prod/llm?region=eu-west-1&profile=ops", "")
	resolve(t, v, "aws-sm:"+smARN, "")
	want := []core.SystemDest{
		{Name: "vault:aws-sm", Hosts: []string{"secretsmanager.us-west-2.amazonaws.com:443"}, AllowPrivate: true},
		{Name: "vault:aws-sm", Hosts: []string{"secretsmanager.eu-west-1.amazonaws.com:443"}, AllowPrivate: true},
		{Name: "vault:aws-sm", Hosts: []string{"secretsmanager.eu-central-1.amazonaws.com:443"}, AllowPrivate: true},
	}
	h.mu.Lock()
	got := slices.Clone(h.dests)
	h.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("dests %+v", got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || !slices.Equal(got[i].Hosts, want[i].Hosts) || got[i].AllowPrivate != want[i].AllowPrivate {
			t.Errorf("dest %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if h.chain("us-west-2", "demo") == nil || h.chain("eu-west-1", "ops") == nil || h.chain("eu-central-1", "demo") == nil {
		t.Errorf("one credential chain per region and profile: %v", h.chains)
	}

	f2, h2 := newAWSFixture(t)
	v2 := f2.open()
	d := h2.deps()
	d.HTTP = func(core.SystemDest) *http.Client {
		return &http.Client{Transport: allowHosts{hosts: []string{"secretsmanager.eu-west-1.amazonaws.com:443"}, srv: h2.sm.srv}}
	}
	v2.SetAWS(d)
	_, err := v2.Resolve(context.Background(), "aws-sm:prod/llm", "", "x")
	if core.CodeOf(err) != core.FailUnreachable || len(h2.sm.requests()) != 0 {
		t.Errorf("a host egress does not admit: %v", err)
	}
	d.HTTP = func(core.SystemDest) *http.Client { return &http.Client{} }
	v2.SetAWS(d)
	if _, err := v2.Resolve(context.Background(), "aws-sm:prod/llm", "", "x"); err == nil || !strings.Contains(err.Error(), "no client transport") {
		t.Errorf("a client without an egress transport is refused: %v", err)
	}
}

func TestAWSRefsSealedVault(t *testing.T) {
	f, h := newAWSFixture(t)
	f.cfg.Vault.KEK = "passphrase"
	v := f.openAWS(h)
	if !v.Status().Sealed {
		t.Fatal("vault not sealed")
	}
	if got := resolve(t, v, "aws-sm:prod/llm", ""); got != llmKey {
		t.Errorf("aws-sm references need no vault key: %q", got)
	}
	if _, err := v.Resolve(context.Background(), "secret:x", "", "x"); !errors.Is(err, core.ErrSealed) {
		t.Errorf("vault secrets stay sealed: %v", err)
	}
}

func TestAWSAccessCheck(t *testing.T) {
	f, h := newAWSFixture(t)
	if fs := runChecks(t, f.openAWS(h)); fs["secrets.aws_access"] != nil {
		t.Errorf("no aws-sm references, no finding: %+v", fs["secrets.aws_access"])
	}

	f, h = newAWSFixture(t)
	f.cfg.Channels.Telegram.Token = "aws-sm:prod/telegram#token"
	f.cfg.Search.BraveAPIKey = "aws-sm:prod/search"
	fs := runChecks(t, f.openAWS(h))
	if c := fs["secrets.aws_access"]; c == nil || c.Severity != core.SevInfo || !strings.Contains(c.Title, "SSO profile demo") ||
		!strings.Contains(c.Detail, "2 references") || !strings.Contains(c.Detail, "never a value") {
		t.Errorf("credentials resolve: %+v", c)
	}

	f, h = newAWSFixture(t)
	f.cfg.Channels.Telegram.Token = "aws-sm:prod/telegram#token"
	h.fail = errors.New("the SSO session for account 123456789012 has expired")
	fs = runChecks(t, f.openAWS(h))
	if c := fs["secrets.aws_access"]; c == nil || c.Severity != core.SevHigh || c.Fix != "aws sso login --profile demo" ||
		!strings.Contains(c.Detail, "channels.telegram.token") || strings.Contains(c.Detail, "123456789012") {
		t.Errorf("credentials fail: %+v", c)
	}

	f, h = newAWSFixture(t)
	f.cfg.Secrets.AWS.Region = ""
	f.cfg.Channels.Telegram.Token = "aws-sm:prod/telegram#token"
	fs = runChecks(t, f.openAWS(h))
	if c := fs["secrets.aws_access"]; c == nil || c.Severity != core.SevHigh || !strings.Contains(c.Detail, "secrets.aws.region") {
		t.Errorf("no region: %+v", c)
	}
	if n := len(h.sm.requests()); n != 0 {
		t.Errorf("the check read %d secrets", n)
	}
}
