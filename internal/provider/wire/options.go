package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// Options configure one provider client. Credentials are callbacks so the
// facade decides how long a resolved secret lives; clients call them per
// request and put the values in headers only.
type Options struct {
	ID       string
	Kind     core.ProviderKind
	BaseURL  string       // without a trailing slash
	HTTP     *http.Client // from core.Egress.SystemClient only
	Key      func(ctx context.Context) (core.Secret, error)
	Headers  func(ctx context.Context) (map[string]core.Secret, error)
	Redact   func(string) string
	Models   []core.ModelInfo // configured models, returned first by Models
	Discover bool             // Models also lists the server's models
}

// URL joins the base URL and a path that starts with "/".
func (o *Options) URL(path string) string { return strings.TrimRight(o.BaseURL, "/") + path }

// key returns the API key, or the zero Secret when none is configured.
func (o *Options) key(ctx context.Context) (core.Secret, error) {
	if o.Key == nil {
		return core.Secret{}, nil
	}
	return o.Key(ctx)
}

// NewRequest builds a request with a JSON body (when body is non-nil), the
// configured extra headers and the API key set by auth (skipped when the key
// is empty, as for local servers without one).
func (o *Options) NewRequest(ctx context.Context, method, url string, body any, auth func(h http.Header, key string)) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			return nil, fmt.Errorf("%s: encoding request: %w", o.ID, err)
		}
		rd = bytes.NewReader(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.ID, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if o.Headers != nil {
		hs, err := o.Headers(ctx)
		if err != nil {
			return nil, o.credentialError(err)
		}
		for k, v := range hs {
			req.Header.Set(k, v.Reveal())
		}
	}
	key, err := o.key(ctx)
	if err != nil {
		return nil, o.credentialError(err)
	}
	if !key.IsZero() && auth != nil {
		auth(req.Header, key.Reveal())
	}
	return req, nil
}

// credentialError reports a secret that could not be resolved. A sealed vault
// is a server-side condition; a missing secret is the operator's to fix.
func (o *Options) credentialError(err error) error {
	kind := core.ErrKindAuth
	if errors.Is(err, core.ErrSealed) {
		kind = core.ErrKindServer
	}
	return &core.ProviderError{Provider: o.ID, Kind: kind, Msg: "credential unavailable: " + o.redact(err.Error()), Err: err}
}

// Redacted applies the client's redactor and the length cap every provider
// message gets, for errors a client builds itself.
func (o *Options) Redacted(s string) string { return o.redact(s) }

func (o *Options) redact(s string) string {
	if o.Redact != nil {
		s = o.Redact(s)
	}
	const max = 512
	if len(s) > max {
		cut := max
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// Classifier maps a non-2xx response to an ErrorKind and the provider's
// message. Each client knows its vendor's error body.
type Classifier func(status int, body []byte) (core.ErrorKind, string)

// HeaderClassifier is a Classifier for vendors that name the error in a
// response header (AWS: X-Amzn-ErrorType).
type HeaderClassifier func(status int, h http.Header, body []byte) (core.ErrorKind, string)

// maxErrorBody bounds how much of an error response is read.
const maxErrorBody = 64 << 10

// MaxBodyBytes bounds a successful response body. Streams carry a token
// limit, but a broken or hostile server could otherwise send without end;
// the longest replies of current models are far below this even with SSE
// framing.
const MaxBodyBytes = 256 << 20

// ErrBodyTooLarge is returned by reads past MaxBodyBytes.
var ErrBodyTooLarge = errors.New("response body exceeds 256 MiB")

type limitedBody struct {
	io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, ErrBodyTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

// Do sends req. A 2xx response is returned for the caller to stream and close;
// anything else becomes a *core.ProviderError (body closed). Transport
// failures are ErrKindNetwork unless ctx ended, in which case the context's
// cause is returned so a /stop is never mistaken for a retryable failure.
func (o *Options) Do(ctx context.Context, req *http.Request, classify Classifier) (*http.Response, error) {
	return o.DoHeader(ctx, req, func(status int, _ http.Header, body []byte) (core.ErrorKind, string) {
		return classify(status, body)
	})
}

// DoHeader is Do with a classifier that also sees the response headers.
func (o *Options) DoHeader(ctx context.Context, req *http.Request, classify HeaderClassifier) (*http.Response, error) {
	// The URL is the operator's base_url and HTTP comes from
	// Egress.SystemClient, which dials only that exact host:port.
	resp, err := o.HTTP.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return nil, o.NetError(ctx, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Body = &limitedBody{ReadCloser: resp.Body, left: MaxBodyBytes}
		return resp, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	kind, msg := classify(resp.StatusCode, resp.Header, body)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return nil, &core.ProviderError{
		Provider:   o.ID,
		Kind:       kind,
		Status:     resp.StatusCode,
		RetryAfter: RetryAfter(resp.Header),
		Msg:        o.redact(msg),
	}
}

// NetError classifies a transport or stream read failure.
func (o *Options) NetError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return &core.ProviderError{Provider: o.ID, Kind: core.ErrKindNetwork, Msg: o.redact(err.Error()), Err: err}
}

// StreamError reports a provider-sent error event inside a 200 stream.
func (o *Options) StreamError(kind core.ErrorKind, msg string) error {
	return &core.ProviderError{Provider: o.ID, Kind: kind, Status: http.StatusOK, Msg: o.redact(msg)}
}

// Malformed reports a stream that cannot be decoded. It counts as a server
// fault so the facade may retry before the first event.
func (o *Options) Malformed(err error) error {
	return &core.ProviderError{Provider: o.ID, Kind: core.ErrKindServer, Status: http.StatusOK, Msg: "malformed stream: " + o.redact(err.Error()), Err: err}
}
