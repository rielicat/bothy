// Package bedrock is the Amazon Bedrock client: ConverseStream (AWS
// event-stream framing) for text, tools, images, cache points and Claude
// reasoning with its signature, and the control plane's model listings. The
// Converse mapping is hand-written like the other clients; the AWS SDK is
// used only for credential resolution, SigV4 signing and event-stream
// decoding, never for its service clients or retries. Owner: M2.
package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// signingName is the SigV4 service name of both Bedrock endpoints.
const signingName = "bedrock"

const eventStreamType = "application/vnd.amazon.eventstream"

// Options configure a Bedrock client. BaseURL is the runtime endpoint
// (RuntimeURL, or a VPC endpoint); the Key and Headers callbacks are unused.
type Options struct {
	wire.Options
	Region      string
	ControlURL  string // "" = ControlURL(Region)
	Credentials aws.CredentialsProvider
	Now         func() time.Time // signing clock; nil = time.Now
}

// Client implements core.Provider.
type Client struct {
	o      Options
	signer *v4.Signer
}

var _ core.Provider = (*Client)(nil)

func New(o Options) *Client {
	o.Kind = core.KindBedrock
	if o.BaseURL == "" {
		o.BaseURL = RuntimeURL(o.Region)
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.ControlURL == "" {
		o.ControlURL = ControlURL(o.Region)
	}
	o.ControlURL = strings.TrimRight(o.ControlURL, "/")
	if o.Now == nil {
		o.Now = time.Now
	}
	parent := o.Redact
	o.Redact = func(s string) string {
		s = ScrubAccounts(s)
		if parent != nil {
			s = parent(s)
		}
		return s
	}
	return &Client{o: o, signer: v4.NewSigner()}
}

func (c *Client) ID() string              { return c.o.ID }
func (c *Client) Kind() core.ProviderKind { return core.KindBedrock }

// invalidator is implemented by credential caches (aws.CredentialsCache and
// Chain) that can drop what they hold.
type invalidator interface{ Invalidate() }

// send signs and sends a request to base + path. path is already escaped
// (escapeSegment for segments that carry ids); body is JSON or nil.
func (c *Client) send(ctx context.Context, method, base, path string, query url.Values, body any) (*http.Response, error) {
	var payload []byte
	if body != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			return nil, fmt.Errorf("%s: encoding request: %w", c.o.ID, err)
		}
		payload = bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	}
	req, err := http.NewRequestWithContext(ctx, method, base, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.o.ID, err)
	}
	raw := strings.TrimRight(req.URL.EscapedPath(), "/") + path
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.o.ID, err)
	}
	req.URL.Path, req.URL.RawPath, req.URL.RawQuery = decoded, raw, query.Encode()
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Body, req.GetBody, req.ContentLength = http.NoBody, nil, 0
	}
	if c.o.Credentials == nil {
		return nil, &core.ProviderError{Provider: c.o.ID, Kind: core.ErrKindAuth, Msg: "no AWS credential chain configured"}
	}
	creds, err := c.o.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, c.credentialError(ctx, err)
	}
	sum := sha256.Sum256(payload)
	if err := c.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), signingName, c.o.Region, c.o.Now()); err != nil {
		return nil, &core.ProviderError{Provider: c.o.ID, Kind: core.ErrKindAuth, Msg: "signing the request: " + c.o.Redacted(err.Error()), Err: err}
	}
	resp, err := c.o.DoHeader(ctx, req, classify)
	var pe *core.ProviderError
	if errors.As(err, &pe) && pe.Kind == core.ErrKindAuth {
		// A refreshed SSO session or rotated role is picked up next time.
		if inv, ok := c.o.Credentials.(invalidator); ok {
			inv.Invalidate()
		}
	}
	return resp, err
}

// credentialError reports AWS credentials that could not be resolved
// (expired SSO session, no role, no metadata service off AWS). They are
// the operator's to fix, so the facade treats them like a rejected key,
// unless an AWS credential service (STS, SSO) was out of reach: that is
// the network, which passes.
func (c *Client) credentialError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	kind := core.ErrKindAuth
	if core.HostFailure(err) != "" {
		kind = core.ErrKindNetwork
	}
	return &core.ProviderError{Provider: c.o.ID, Kind: kind,
		Msg: "AWS credentials unavailable: " + c.o.Redacted(err.Error()), Err: err}
}

// escapeSegment escapes a path segment the way the AWS SDKs do: everything
// but RFC 3986 unreserved characters, so ':' and '/' in model ids and ARNs
// survive SigV4's canonical URI.
func escapeSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if 'A' <= ch && ch <= 'Z' || 'a' <= ch && ch <= 'z' || '0' <= ch && ch <= '9' || ch == '-' || ch == '.' || ch == '_' || ch == '~' {
			b.WriteByte(ch)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", ch)
	}
	return b.String()
}

func (c *Client) Stream(ctx context.Context, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	return c.stream(ctx, r.Model, buildRequest(r), on)
}

func (c *Client) stream(ctx context.Context, model string, body request, on func(core.StreamEvent) error) (*core.Response, error) {
	if model == "" || model == "." || model == ".." {
		// "." and ".." survive escapeSegment and would move the signed
		// request to another path.
		return nil, &core.ProviderError{Provider: c.o.ID, Kind: core.ErrKindInvalidRequest, Msg: fmt.Sprintf("invalid model id %q", model)}
	}
	resp, err := c.send(ctx, http.MethodPost, c.o.BaseURL, "/model/"+escapeSegment(model)+"/converse-stream", nil, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, eventStreamType) {
		return nil, c.o.Malformed(fmt.Errorf("unexpected content type %q", ct))
	}
	return c.read(ctx, resp.Body, model, on)
}
