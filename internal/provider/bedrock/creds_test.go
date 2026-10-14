package bedrock

import (
	"context"
	"crypto/sha1" //nolint:gosec // the SDK names SSO token cache files by SHA-1
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// awsEnv isolates the AWS SDK from the developer's environment: no AWS_*
// variables, an empty home and empty shared config files unless written.
type awsEnv struct {
	t      *testing.T
	home   string
	config string
	creds  string
}

var awsVars = []string{
	"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN", "AWS_ACCESS_KEY", "AWS_SECRET_KEY", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AWS_EC2_METADATA_DISABLED", "AWS_EC2_METADATA_SERVICE_ENDPOINT",
	"AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE", "AWS_USE_FIPS_ENDPOINT", "AWS_USE_DUALSTACK_ENDPOINT", "AWS_ENDPOINT_URL",
	"AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_SSO", "AWS_CA_BUNDLE", "AWS_SDK_LOAD_CONFIG", "AWS_STS_REGIONAL_ENDPOINTS",
	"AWS_RETRY_MODE", "AWS_MAX_ATTEMPTS", "AWS_DEFAULTS_MODE", "AWS_EC2_METADATA_V1_DISABLED",
}

func newAWSEnv(t *testing.T) *awsEnv {
	t.Helper()
	for _, v := range awsVars {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	home := t.TempDir()
	e := &awsEnv{t: t, home: home, config: filepath.Join(home, ".aws", "config"), creds: filepath.Join(home, ".aws", "credentials")}
	if err := os.MkdirAll(filepath.Join(home, ".aws", "sso", "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AWS_CONFIG_FILE", e.config)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", e.creds)
	return e
}

func (e *awsEnv) write(path, content string) {
	e.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// fakeAWS answers AWS service requests by host without a network and
// records them; hosts not in allowed are refused like an egress system
// client would.
type fakeAWS struct {
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	seen     []string
	allowed  map[string]bool // nil = any
}

func (f *fakeAWS) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
		if req.URL.Scheme == "http" {
			port = "80"
		}
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	f.mu.Lock()
	f.seen = append(f.seen, method+" "+req.URL.Scheme+"://"+req.URL.Host+req.URL.RequestURI())
	h := f.handlers[host]
	allowed := f.allowed == nil || f.allowed[host+":"+port]
	f.mu.Unlock()
	if !allowed {
		return nil, fmt.Errorf("egress: %s is not a configured host", host)
	}
	if h == nil {
		return nil, fmt.Errorf("no fake for %s", host)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Result(), nil
}

func (f *fakeAWS) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// chainWith builds a chain whose system and metadata clients are fakes.
func chainWith(o ChainOptions, system, metadata *fakeAWS) (*Chain, *[]string) {
	var hosts []string
	o.System = func(hs []string) *http.Client {
		hosts = hs
		system.mu.Lock()
		system.allowed = map[string]bool{}
		for _, h := range hs {
			system.allowed[h] = true
		}
		system.mu.Unlock()
		return &http.Client{Transport: system}
	}
	if metadata != nil {
		o.Metadata = &http.Client{Transport: metadata}
	}
	return NewChain(o), &hosts
}

func TestChainEnvironmentAndSharedFiles(t *testing.T) {
	ctx := context.Background()
	e := newAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDENVEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	sys := &fakeAWS{}
	c, _ := chainWith(ChainOptions{Region: "us-west-2"}, sys, nil)
	creds, err := c.Retrieve(ctx)
	if err != nil || creds.AccessKeyID != "AKIDENVEXAMPLE" {
		t.Fatalf("env: %v %+v", err, creds.AccessKeyID)
	}
	if src, err := c.Describe(ctx); err != nil || src != "access keys in environment variables" {
		t.Fatalf("describe %q %v", src, err)
	}
	if strings.Contains(fmt.Sprint(c.Describe(ctx)), "env-secret") {
		t.Fatal("description carries the secret")
	}

	os.Unsetenv("AWS_ACCESS_KEY_ID")
	os.Unsetenv("AWS_SECRET_ACCESS_KEY")
	e.write(e.creds, "[dev]\naws_access_key_id = AKIDFILEEXAMPLE\naws_secret_access_key = file-secret\n")
	e.write(e.config, "[profile dev]\nregion = eu-west-1\n")
	c, _ = chainWith(ChainOptions{Profile: "dev"}, sys, nil)
	src, err := c.Describe(ctx)
	if err != nil || src != "shared credentials file, profile dev" {
		t.Fatalf("shared file: %q %v", src, err)
	}
	if c.Region(ctx) != "eu-west-1" || c.Profile(ctx) != "dev" {
		t.Fatalf("region %q profile %q", c.Region(ctx), c.Profile(ctx))
	}

	c, _ = chainWith(ChainOptions{Region: "us-west-2", Profile: "missing"}, sys, nil)
	if _, err := c.Retrieve(ctx); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing profile: %v", err)
	}
	// A fixed config is picked up on the next call.
	e.write(e.creds, "[dev]\naws_access_key_id = AKIDFILEEXAMPLE\naws_secret_access_key = file-secret\n[missing]\naws_access_key_id = AKIDLATE\naws_secret_access_key = s\n")
	if creds, err := c.Retrieve(ctx); err != nil || creds.AccessKeyID != "AKIDLATE" {
		t.Fatalf("after fixing the profile: %v", err)
	}
	if n := len(sys.requests()); n != 0 {
		t.Fatalf("static credentials made %d requests", n)
	}
}

func TestChainSSOSession(t *testing.T) {
	ctx := context.Background()
	e := newAWSEnv(t)
	e.write(e.config, `[profile work]
sso_session = corp
sso_account_id = 111122223333
sso_role_name = Developer
region = us-west-2

[sso-session corp]
sso_region = eu-west-1
sso_start_url = https://example.awsapps.com/start
sso_registration_scopes = sso:account:access
`)
	sum := sha1.Sum([]byte("corp")) //nolint:gosec // see import
	e.write(filepath.Join(e.home, ".aws", "sso", "cache", hex.EncodeToString(sum[:])+".json"),
		`{"accessToken":"sso-bearer-CANARY","expiresAt":"2099-01-01T00:00:00Z","region":"eu-west-1","startUrl":"https://example.awsapps.com/start"}`)
	calls := 0
	sys := &fakeAWS{handlers: map[string]http.HandlerFunc{
		"portal.sso.eu-west-1.amazonaws.com": func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/federation/credentials" || r.Header.Get("X-Amz-Sso_bearer_token") != "sso-bearer-CANARY" ||
				r.URL.Query().Get("account_id") != "111122223333" || r.URL.Query().Get("role_name") != "Developer" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			exp := time.Now().Add(time.Hour).UnixMilli()
			fmt.Fprintf(w, `{"roleCredentials":{"accessKeyId":"AKIDSSOEXAMPLE","secretAccessKey":"sso-secret","sessionToken":"sso-token","expiration":%d}}`, exp)
		},
	}}
	c, hosts := chainWith(ChainOptions{Region: "us-west-2", Profile: "work"}, sys, nil)
	src, err := c.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if src != "AWS SSO, profile work (session corp)" {
		t.Fatalf("describe %q", src)
	}
	want := []string{"oidc.eu-west-1.amazonaws.com:443", "portal.sso.eu-west-1.amazonaws.com:443", "sts.amazonaws.com:443", "sts.us-west-2.amazonaws.com:443"}
	if !slices.Equal(*hosts, want) {
		t.Fatalf("credential hosts %v", *hosts)
	}
	creds, err := c.Retrieve(ctx)
	if err != nil || creds.AccessKeyID != "AKIDSSOEXAMPLE" || calls != 1 {
		t.Fatalf("cached: %v %d calls", err, calls)
	}
	c.Invalidate()
	if _, err := c.Retrieve(ctx); err != nil || calls != 2 {
		t.Fatalf("after Invalidate: %v %d calls", err, calls)
	}
}

// imdsFake answers the IMDSv2 role flow.
func imdsFake(region string) *fakeAWS {
	h := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", r.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds"))
			fmt.Fprint(w, "IMDS-TOKEN")
		case r.Header.Get("X-Aws-Ec2-Metadata-Token") != "IMDS-TOKEN":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
			fmt.Fprint(w, "bothy-host\n")
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/bothy-host":
			fmt.Fprintf(w, `{"Code":"Success","AccessKeyId":"AKIDIMDSEXAMPLE","SecretAccessKey":"imds-secret","Token":"imds-token","Expiration":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case r.URL.Path == "/latest/meta-data/placement/region":
			fmt.Fprint(w, region)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return &fakeAWS{handlers: map[string]http.HandlerFunc{"169.254.169.254": h}}
}

func TestChainInstanceRole(t *testing.T) {
	ctx := context.Background()
	newAWSEnv(t)
	sys, meta := &fakeAWS{}, imdsFake("eu-central-1")
	c, _ := chainWith(ChainOptions{IMDSRegion: true}, sys, meta)
	src, err := c.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if src != "EC2 instance role (IMDSv2)" || c.Region(ctx) != "eu-central-1" {
		t.Fatalf("describe %q region %q", src, c.Region(ctx))
	}
	for _, r := range meta.requests() {
		if !strings.HasPrefix(r, "PUT http://169.254.169.254/latest/api/token") && !strings.Contains(r, "/latest/meta-data/") {
			t.Fatalf("unexpected metadata request %s", r)
		}
	}
	if n := len(sys.requests()); n != 0 {
		t.Fatalf("metadata requests went to the system client: %v", sys.requests())
	}

	// Without the credentials client the instance role is unreachable.
	c, _ = chainWith(ChainOptions{Region: "us-west-2"}, sys, nil)
	if _, err := c.Retrieve(ctx); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("no metadata client: %v", err)
	}
}

func TestChainWebIdentityAndNoRetries(t *testing.T) {
	ctx := context.Background()
	e := newAWSEnv(t)
	token := filepath.Join(e.home, "token")
	e.write(token, "eyJ.web-identity.CANARY")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", token)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::111122223333:role/bothy")
	t.Setenv("AWS_REGION", "us-east-2")
	fail := true
	sys := &fakeAWS{handlers: map[string]http.HandlerFunc{
		"sts.us-east-2.amazonaws.com": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code><Message>try later</Message></Error></ErrorResponse>`)
				return
			}
			if !strings.Contains(string(b), "Action=AssumeRoleWithWebIdentity") || !strings.Contains(string(b), "WebIdentityToken=eyJ.web-identity.CANARY") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult>`+
				`<Credentials><AccessKeyId>AKIDWEBEXAMPLE</AccessKeyId><SecretAccessKey>web-secret</SecretAccessKey><SessionToken>web-token</SessionToken>`+
				`<Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::111122223333:assumed-role/bothy/s</Arn><AssumedRoleId>AROAEXAMPLE:s</AssumedRoleId></AssumedRoleUser>`+
				`</AssumeRoleWithWebIdentityResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></AssumeRoleWithWebIdentityResponse>`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		},
	}}
	c, hosts := chainWith(ChainOptions{}, sys, nil)
	if _, err := c.Retrieve(ctx); err == nil {
		t.Fatal("a failing STS must fail the retrieval")
	}
	if n := len(sys.requests()); n != 1 {
		t.Fatalf("SDK retried: %d requests", n)
	}
	if !slices.Contains(*hosts, "sts.us-east-2.amazonaws.com:443") {
		t.Fatalf("hosts %v", *hosts)
	}
	fail = false
	src, err := c.Describe(ctx)
	if err != nil || src != "web identity token from the environment (IRSA)" {
		t.Fatalf("describe %q %v", src, err)
	}
	if strings.Contains(src, "111122223333") {
		t.Fatal("the description names the account")
	}
}

func TestChainContainerCredentials(t *testing.T) {
	ctx := context.Background()
	newAWSEnv(t)
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/v2/credentials/task-1")
	body := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"AccessKeyId":"AKIDTASKEXAMPLE","SecretAccessKey":"task-secret","Token":"task-token","Expiration":%q}`,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}
	meta := &fakeAWS{handlers: map[string]http.HandlerFunc{"169.254.170.2": body, "localhost": body}}
	c, _ := chainWith(ChainOptions{}, &fakeAWS{}, meta)
	src, err := c.Describe(ctx)
	if err != nil || src != "container credentials (ECS task role or EKS Pod Identity)" {
		t.Fatalf("describe %q %v", src, err)
	}
	if r := meta.requests(); len(r) != 1 || r[0] != "GET http://169.254.170.2/v2/credentials/task-1" {
		t.Fatalf("requests %v", r)
	}

	// A full URI on a host name goes to the credentials client too.
	os.Unsetenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://localhost:9911/creds")
	meta.seen = nil
	c, _ = chainWith(ChainOptions{ContainerHost: "localhost"}, &fakeAWS{}, meta)
	if _, err := c.Retrieve(ctx); err != nil {
		t.Fatal(err)
	}
	if r := meta.requests(); len(r) != 1 || r[0] != "GET http://localhost:9911/creds" {
		t.Fatalf("requests %v", r)
	}
}

func TestCredentialHosts(t *testing.T) {
	ctx := context.Background()
	e := newAWSEnv(t)
	t.Setenv("AWS_USE_FIPS_ENDPOINT", "true")
	t.Setenv("AWS_USE_DUALSTACK_ENDPOINT", "true")
	e.write(e.creds, "[base]\naws_access_key_id = AKIDBASE\naws_secret_access_key = s\n")
	e.write(e.config, `[profile base]
region = eu-north-1

[profile role]
role_arn = arn:aws:iam::111122223333:role/r
source_profile = base
region = ap-south-1
endpoint_url = https://proxy.example.com:8443

[profile me]
login_session = arn:aws:iam::111122223333:user/me
region = eu-west-3

[profile tool]
credential_process = /usr/local/bin/aws-vault export --format=json dev
`)
	sys := &fakeAWS{}
	c, made := chainWith(ChainOptions{Region: "us-west-2", Profile: "role"}, sys, nil)
	hosts := c.CredentialHosts(ctx)
	if len(*made) != 0 {
		t.Fatal("a system client was made before any credential service was called")
	}
	for _, h := range []string{"sts.us-west-2.amazonaws.com:443", "sts-fips.us-west-2.amazonaws.com:443", "sts.us-west-2.api.aws:443",
		"sts.ap-south-1.amazonaws.com:443", "sts.eu-north-1.amazonaws.com:443", "proxy.example.com:8443", "sts.amazonaws.com:443"} {
		if !slices.Contains(hosts, h) {
			t.Errorf("missing %s in %v", h, hosts)
		}
	}
	for _, h := range hosts {
		if strings.Contains(h, "portal.sso") || strings.Contains(h, "signin") {
			t.Errorf("unneeded credential host %s", h)
		}
	}

	c, _ = chainWith(ChainOptions{Region: "us-west-2", Profile: "me"}, &fakeAWS{}, nil)
	hosts = c.CredentialHosts(ctx)
	for _, h := range []string{"us-west-2.signin.aws.amazon.com:443", "us-west-2.oauth.signin.aws:443", "eu-west-3.oauth.signin.aws:443"} {
		if !slices.Contains(hosts, h) {
			t.Errorf("missing %s in %v", h, hosts)
		}
	}

	// credential_process would run a program outside egress control.
	c, _ = chainWith(ChainOptions{Region: "us-west-2", Profile: "tool"}, &fakeAWS{}, nil)
	if _, err := c.Retrieve(ctx); err == nil || !strings.Contains(err.Error(), "credential_process") {
		t.Fatalf("credential_process: %v", err)
	}
}

func TestDescribeSources(t *testing.T) {
	for _, tc := range []struct {
		sources []aws.CredentialSource
		profile string
		want    string
	}{
		{[]aws.CredentialSource{aws.CredentialSourceProfileSourceProfile, aws.CredentialSourceSSO, aws.CredentialSourceProfileSSO, aws.CredentialSourceSTSAssumeRole}, "admin", "role assumed with AWS SSO, profile admin"},
		{[]aws.CredentialSource{aws.CredentialSourceProfileLogin, aws.CredentialSourceLogin}, "me", "aws login session, profile me"},
		{[]aws.CredentialSource{aws.CredentialSourceProfileProcess, aws.CredentialSourceProcess}, "vault", "credential_process, profile vault"},
		{[]aws.CredentialSource{aws.CredentialSourceProfileSTSWebIDToken, aws.CredentialSourceSTSAssumeRoleWebID}, "k8s", "web identity token, profile k8s"},
		{[]aws.CredentialSource{aws.CredentialSourceProfileNamedProvider, aws.CredentialSourceIMDS, aws.CredentialSourceSTSAssumeRole}, "x", "role assumed with EC2 instance role (IMDSv2)"},
		{nil, "", "AWS default credential chain"},
	} {
		if got := describe(tc.sources, tc.profile, ""); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.sources, got, tc.want)
		}
	}
}
