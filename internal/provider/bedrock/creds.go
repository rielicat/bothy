package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/endpointcreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

// credsExpiryWindow is how long before their expiry cached AWS credentials
// are renewed.
const credsExpiryWindow = 5 * time.Minute

// ChainOptions configure the AWS default credential chain for one provider.
type ChainOptions struct {
	Region  string // "" = the region the AWS config names
	Profile string // "" = AWS_PROFILE, else the default chain
	// System returns the egress client for exactly these AWS credential
	// service hosts (STS, SSO portal and OIDC, sign-in), as "host:443".
	System func(hosts []string) *http.Client
	// Metadata is the egress credentials client for the instance metadata
	// service and the container credential endpoint; nil leaves both
	// unreachable.
	Metadata *http.Client
	// ContainerHost is the host of AWS_CONTAINER_CREDENTIALS_FULL_URI, which
	// goes through Metadata like the link-local endpoints.
	ContainerHost string
	// IMDSRegion looks the region up in the instance metadata when neither
	// Region nor the AWS config names one (Detect on an EC2 host).
	IMDSRegion bool
}

// Chain resolves credentials through the AWS SDK's default chain: the
// environment, shared config and credentials files (SSO sessions, assumed
// roles, credential_process, aws login), web identity (IRSA), container
// credentials (ECS, EKS Pod Identity) and EC2 IMDSv2. It loads the AWS
// config lazily and again after a failed load, so a fixed ~/.aws/config is
// picked up without a restart. SDK retries are off; the facade owns them.
type Chain struct {
	o      ChainOptions
	router *router

	mu           sync.Mutex
	cfg          *aws.Config
	region       string
	profile      string
	instanceRole string
}

var _ aws.CredentialsProvider = (*Chain)(nil)

func NewChain(o ChainOptions) *Chain {
	return &Chain{o: o, router: &router{metadata: o.Metadata, containerHost: strings.ToLower(o.ContainerHost)}}
}

func (c *Chain) load(ctx context.Context) (*aws.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg != nil {
		return c.cfg, nil
	}
	opts := []func(*config.LoadOptions) error{
		config.WithHTTPClient(c.router),
		config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }),
		// The container credential provider ignores the config's HTTP client
		// and would dial the network directly.
		config.WithEndpointCredentialOptions(func(o *endpointcreds.Options) { o.HTTPClient = c.router }),
		// Without a window SSO and role credentials are used up to their
		// last second, and a request signed just before expiry fails as an
		// auth error the facade does not retry on the same model.
		config.WithCredentialsCacheOptions(func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = credsExpiryWindow }),
	}
	if c.o.Region != "" {
		opts = append(opts, config.WithRegion(c.o.Region))
	}
	if c.o.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(c.o.Profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading the AWS configuration: %w", err)
	}
	if cfg.Credentials == nil {
		return nil, errors.New("the AWS configuration resolves no credential provider")
	}
	sc := sharedConfig(cfg)
	for p, depth := &sc, 0; p != nil && depth < 8; p, depth = p.Source, depth+1 {
		if p.CredentialProcess != "" {
			return nil, fmt.Errorf("profile %q uses credential_process, which Bothy does not run: the program would act outside its egress control; use SSO, a role or static keys instead", p.Profile)
		}
	}
	c.profile = sc.Profile
	c.router.setHosts(credentialHosts(cfg, sc), c.o.System)
	if cfg.Region == "" && c.o.IMDSRegion && c.o.Metadata != nil {
		if r := imdsRegion(ctx, cfg); ValidRegion(r) {
			cfg.Region = r
		}
	}
	c.region = cfg.Region
	c.cfg = &cfg
	return c.cfg, nil
}

// imdsRegion reads the instance's region from placement/region; the SDK's
// own lookup reads the identity document, which the credentials dialer
// refuses.
func imdsRegion(ctx context.Context, cfg aws.Config) string {
	out, err := imds.NewFromConfig(cfg).GetMetadata(ctx, &imds.GetMetadataInput{Path: "placement/region"})
	if err != nil {
		return ""
	}
	defer out.Content.Close()
	b, err := io.ReadAll(io.LimitReader(out.Content, 64))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Retrieve implements aws.CredentialsProvider.
func (c *Chain) Retrieve(ctx context.Context) (aws.Credentials, error) {
	cfg, err := c.load(ctx)
	if err != nil {
		return aws.Credentials{}, err
	}
	return cfg.Credentials.Retrieve(ctx)
}

// Invalidate drops the cached credentials; the next Retrieve resolves
// fresh ones.
func (c *Chain) Invalidate() {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if cfg == nil {
		return
	}
	if inv, ok := cfg.Credentials.(invalidator); ok {
		inv.Invalidate()
	}
}

// Region is the region the AWS config resolved ("" before a load or when it
// names none).
func (c *Chain) Region(ctx context.Context) string {
	if _, err := c.load(ctx); err != nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.region
}

// CloseIdleConnections closes the pooled connections of the clients the
// chain made, for chains that are dropped (a probe, a rebuilt provider).
func (c *Chain) CloseIdleConnections() {
	c.router.mu.Lock()
	sys := c.router.system
	c.router.mu.Unlock()
	if sys != nil {
		sys.CloseIdleConnections()
	}
	if c.router.metadata != nil {
		c.router.metadata.CloseIdleConnections()
	}
}

// CredentialHosts lists the AWS credential service hosts ("host:443") the
// chain may reach for its configuration.
func (c *Chain) CredentialHosts(ctx context.Context) []string {
	if _, err := c.load(ctx); err != nil {
		return nil
	}
	c.router.mu.Lock()
	defer c.router.mu.Unlock()
	return append([]string(nil), c.router.hosts...)
}

// Profile is the shared-config profile the chain selected.
func (c *Chain) Profile(ctx context.Context) string {
	if _, err := c.load(ctx); err != nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.profile
}

// Describe resolves credentials and says in plain words where they came
// from ("SSO profile acme-dev", "instance role bothy-prod", "IRSA role
// bothy"), never what they are: role names, but no ARN, account id or
// session.
func (c *Chain) Describe(ctx context.Context) (string, error) {
	cfg, err := c.load(ctx)
	if err != nil {
		return "", err
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return "", err
	}
	var sources []aws.CredentialSource
	if ps, ok := cfg.Credentials.(aws.CredentialProviderSource); ok {
		sources = ps.ProviderSources()
	}
	env := envConfig(*cfg)
	src := credSource{roleARN: sharedConfig(*cfg).RoleARN, envRoleARN: env.RoleARN, containerFull: env.ContainerCredentialsEndpoint}
	if slices.Contains(sources, aws.CredentialSourceIMDS) {
		src.instanceRole = c.instanceRoleName(ctx, *cfg)
	}
	c.mu.Lock()
	src.profile = c.profile
	c.mu.Unlock()
	return describe(sources, src), nil
}

// instanceRoleName asks the instance metadata which role the instance
// carries (a name only; the listing holds no credentials), once per chain.
func (c *Chain) instanceRoleName(ctx context.Context, cfg aws.Config) string {
	c.mu.Lock()
	name := c.instanceRole
	c.mu.Unlock()
	if name != "" {
		return name
	}
	out, err := imds.NewFromConfig(cfg).GetMetadata(ctx, &imds.GetMetadataInput{Path: "iam/security-credentials/"})
	if err != nil {
		return ""
	}
	defer out.Content.Close()
	b, err := io.ReadAll(io.LimitReader(out.Content, 256))
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if !roleNameRe.MatchString(first) {
		return ""
	}
	c.mu.Lock()
	c.instanceRole = first
	c.mu.Unlock()
	return first
}

var roleNameRe = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,128}$`)

// credSource is what describe knows besides the providers that resolved.
type credSource struct {
	profile       string
	roleARN       string // the profile's role_arn
	envRoleARN    string // AWS_ROLE_ARN (IRSA)
	containerFull string // AWS_CONTAINER_CREDENTIALS_FULL_URI
	instanceRole  string
}

// roleName is the last path segment of an IAM role ARN.
func roleName(arn string) string {
	_, rest, ok := strings.Cut(arn, ":role/")
	if !ok {
		return ""
	}
	if i := strings.LastIndexByte(rest, '/'); i >= 0 {
		rest = rest[i+1:]
	}
	if !roleNameRe.MatchString(rest) {
		return ""
	}
	return rest
}

// podIdentityHosts are the EKS Pod Identity agent's addresses.
var podIdentityHosts = []string{"169.254.170.23", "[fd00:ec2::23]"}

func describe(sources []aws.CredentialSource, src credSource) string {
	has := func(s ...aws.CredentialSource) bool {
		for _, x := range s {
			if slices.Contains(sources, x) {
				return true
			}
		}
		return false
	}
	profile := func(what string) string {
		if src.profile == "" {
			return what
		}
		return what + " profile " + src.profile
	}
	named := func(what, name string) string {
		if name == "" {
			return what
		}
		return what + " " + name
	}
	var base string
	switch {
	case has(aws.CredentialSourceSSO, aws.CredentialSourceSSOLegacy):
		base = profile("SSO")
	case has(aws.CredentialSourceLogin):
		base = profile("AWS sign-in")
	case has(aws.CredentialSourceProcess):
		base = profile("credential process")
	case has(aws.CredentialSourceEnvVarsSTSWebIDToken):
		base = named("IRSA role", roleName(src.envRoleARN))
	case has(aws.CredentialSourceProfileSTSWebIDToken):
		base = profile("web identity")
	case has(aws.CredentialSourceHTTP):
		base = "ECS task role"
		for _, h := range podIdentityHosts {
			if strings.Contains(src.containerFull, "//"+h) {
				base = "EKS Pod Identity role"
			}
		}
		if src.containerFull != "" && base == "ECS task role" {
			base = "container role"
		}
	case has(aws.CredentialSourceIMDS):
		base = named("instance role", src.instanceRole)
	case has(aws.CredentialSourceEnvVars):
		base = "access keys in the environment"
	case has(aws.CredentialSourceProfile):
		base = "access keys in the AWS credentials file"
		if src.profile != "" {
			base = "access keys in profile " + src.profile
		}
	default:
		base = profile("AWS default credentials")
		if src.profile != "" {
			base = "profile " + src.profile
		}
	}
	if has(aws.CredentialSourceSTSAssumeRole) && !has(aws.CredentialSourceEnvVarsSTSWebIDToken, aws.CredentialSourceProfileSTSWebIDToken) {
		base = named("role", roleName(src.roleARN)) + " assumed with " + base
	}
	return base
}

func sharedConfig(cfg aws.Config) config.SharedConfig {
	for _, s := range cfg.ConfigSources {
		switch v := s.(type) {
		case config.SharedConfig:
			return v
		case *config.SharedConfig:
			if v != nil {
				return *v
			}
		}
	}
	return config.SharedConfig{}
}

func envConfig(cfg aws.Config) config.EnvConfig {
	for _, s := range cfg.ConfigSources {
		switch v := s.(type) {
		case config.EnvConfig:
			return v
		case *config.EnvConfig:
			if v != nil {
				return *v
			}
		}
	}
	return config.EnvConfig{}
}

// credentialHosts lists the AWS services the credential chain may call for
// this configuration: STS in every region the profiles name (assumed roles,
// web identity), the SSO portal and OIDC endpoints of SSO sessions, the
// sign-in endpoints of aws login sessions, their FIPS and dual-stack forms
// when configured, and a configured AWS_ENDPOINT_URL.
func credentialHosts(cfg aws.Config, sc config.SharedConfig) []string {
	env := envConfig(cfg)
	fips := env.UseFIPSEndpoint == aws.FIPSEndpointStateEnabled || sc.UseFIPSEndpoint == aws.FIPSEndpointStateEnabled
	dual := env.UseDualStackEndpoint == aws.DualStackEndpointStateEnabled || sc.UseDualStackEndpoint == aws.DualStackEndpointStateEnabled
	set := map[string]bool{"sts.amazonaws.com:443": true}
	service := func(name, region string) {
		if !ValidRegion(region) {
			return
		}
		suffix := DNSSuffix(region)
		set[name+"."+region+"."+suffix+":443"] = true
		if fips {
			set[name+"-fips."+region+"."+suffix+":443"] = true
		}
		if dual {
			set[name+"."+region+".api.aws:443"] = true
		}
	}
	login := func(region string) {
		if ValidRegion(region) {
			set[region+".signin.aws.amazon.com:443"] = true
			set[region+".oauth.signin.aws:443"] = true
		}
	}
	service("sts", cfg.Region)
	for p, depth := &sc, 0; p != nil && depth < 8; p, depth = p.Source, depth+1 {
		service("sts", p.Region)
		for _, r := range []string{p.SSORegion, ssoSessionRegion(p)} {
			service("portal.sso", r)
			service("oidc", r)
		}
		if p.LoginSession != "" {
			login(cfg.Region)
			login(p.Region)
		}
	}
	for _, e := range []string{env.BaseEndpoint, sc.BaseEndpoint} {
		if u, err := url.Parse(e); e != "" && err == nil && u.Scheme == "https" && u.Hostname() != "" {
			port := u.Port()
			if port == "" {
				port = "443"
			}
			set[net.JoinHostPort(strings.ToLower(u.Hostname()), port)] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	slices.Sort(out)
	return out
}

func ssoSessionRegion(p *config.SharedConfig) string {
	if p.SSOSession == nil {
		return ""
	}
	return p.SSOSession.SSORegion
}

// router is the aws.HTTPClient the SDK's credential providers use: the
// instance metadata service, container endpoints (IP literals and the full
// URI's host) go to the egress credentials client, everything else to the
// system client for the credential service hosts.
type router struct {
	metadata      *http.Client
	containerHost string

	mu     sync.Mutex
	hosts  []string
	mk     func(hosts []string) *http.Client
	system *http.Client
}

// setHosts records the credential service hosts; the system client for them
// is made on the first request that needs it, so a chain resolved from the
// environment or IMDS never asks egress for one.
func (r *router) setHosts(hosts []string, mk func([]string) *http.Client) {
	r.mu.Lock()
	r.hosts, r.mk, r.system = hosts, mk, nil
	r.mu.Unlock()
}

func (r *router) Do(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Hostname())
	if net.ParseIP(host) != nil || host != "" && host == r.containerHost {
		if r.metadata == nil {
			return nil, fmt.Errorf("instance and container credentials are not available to this provider (%s)", host)
		}
		return r.metadata.Do(req) //nolint:gosec // G704: the credentials client admits only the credential endpoints
	}
	r.mu.Lock()
	if r.system == nil && r.mk != nil {
		r.system = r.mk(r.hosts)
	}
	sys := r.system
	r.mu.Unlock()
	if sys == nil {
		return nil, fmt.Errorf("no egress client for AWS credential service %s", host)
	}
	return sys.Do(req) //nolint:gosec // G704: the system client dials only the computed credential hosts
}
