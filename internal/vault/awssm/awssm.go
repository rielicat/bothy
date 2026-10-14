// Package awssm reads one secret from AWS Secrets Manager (GetSecretValue)
// for the vault's aws-sm references. The request is built and signed by
// hand like the bedrock client's: the AWS SDK supplies only the credential
// type and the SigV4 signer, and the credential chain itself is the bedrock
// provider's, handed in by cmd/bothy. Owner: M1.
package awssm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/rielicat/bothy/internal/core"
)

const (
	signingName = "secretsmanager"
	target      = "secretsmanager.GetSecretValue"
	// maxBody covers a 64 KiB SecretBinary after base64 plus the JSON around it.
	maxBody = 256 << 10
)

// Credentials is the AWS credential chain a Client signs with.
type Credentials interface {
	aws.CredentialsProvider
	// Describe resolves the credentials and says in plain words where they
	// come from ("SSO profile dev", "ECS task role"), never what they are.
	Describe(ctx context.Context) (string, error)
}

// DNSSuffix is the endpoint domain of the region's partition.
func DNSSuffix(region string) string {
	if strings.HasPrefix(region, "cn-") {
		return "amazonaws.com.cn"
	}
	return "amazonaws.com"
}

// Host is the Secrets Manager endpoint of a region as "host:443", the one
// host its egress client may reach.
func Host(region string) string {
	return "secretsmanager." + region + "." + DNSSuffix(region) + ":443"
}

// Options configure a Client.
type Options struct {
	Region      string
	Credentials aws.CredentialsProvider
	// HTTP is the egress system client for exactly Host(Region). A client
	// without its own transport is refused: http.DefaultTransport would
	// dial outside every egress rule.
	HTTP *http.Client
	Now  func() time.Time // signing clock; nil = time.Now
}

// Client calls GetSecretValue in one region.
type Client struct {
	o      Options
	hc     *http.Client
	signer *v4.Signer
}

func New(o Options) (*Client, error) {
	switch {
	case o.Region == "":
		return nil, errors.New("awssm: no region")
	case o.Credentials == nil:
		return nil, errors.New("awssm: no AWS credential chain")
	case o.HTTP == nil || o.HTTP.Transport == nil:
		return nil, errors.New("awssm: egress returned no client transport")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	hc := &http.Client{
		Transport:     o.HTTP.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{o: o, hc: hc, signer: v4.NewSigner()}, nil
}

// Value is a secret's current version. Exactly one of String and Binary is
// set, as Secrets Manager stores one or the other.
type Value struct {
	ARN       string
	Name      string
	VersionID string
	String    []byte
	Binary    []byte
}

// Error is a failed GetSecretValue. Msg carries AWS's own message with
// account ids and role sessions removed; it never contains a secret value.
type Error struct {
	Code    core.FailureCode
	AWSCode string // "ResourceNotFoundException"; "" for credential and transport failures
	Status  int
	Msg     string
	Err     error
}

func (e *Error) Error() string {
	switch {
	case e.AWSCode != "" && e.Msg != "":
		return e.AWSCode + ": " + e.Msg
	case e.AWSCode != "":
		return e.AWSCode
	}
	return e.Msg
}

func (e *Error) Unwrap() error                 { return e.Err }
func (e *Error) FailureCode() core.FailureCode { return e.Code }

// GetSecretValue reads the current version (AWSCURRENT) of secretID, a
// name or an ARN.
func (c *Client) GetSecretValue(ctx context.Context, secretID string) (*Value, error) {
	payload, err := json.Marshal(struct {
		SecretID string `json:"SecretId"`
	}{secretID})
	if err != nil {
		return nil, err
	}
	endpoint := "https://" + strings.TrimSuffix(Host(c.o.Region), ":443") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	creds, err := c.o.Credentials.Retrieve(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, &Error{Code: core.FailAWSNoCredentials, Msg: ScrubAccounts(err.Error()), Err: err}
	}
	sum := sha256.Sum256(payload)
	if err := c.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), signingName, c.o.Region, c.o.Now()); err != nil {
		return nil, &Error{Code: core.FailAWSNoCredentials, Msg: "signing the request: " + ScrubAccounts(err.Error()), Err: err}
	}
	resp, err := c.hc.Do(req) //nolint:gosec // G704: the egress system client dials only Host(region)
	if err != nil {
		code := core.TransportFailure(err)
		if code == "" {
			code = core.FailUnreachable
		}
		return nil, &Error{Code: code, Msg: transportMessage(err), Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, &Error{Code: core.FailUnreachable, Msg: "reading the response: " + transportMessage(err), Err: err}
	}
	if len(body) > maxBody {
		return nil, &Error{Code: core.FailProviderError, Status: resp.StatusCode, Msg: "the response is larger than a secret can be"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classify(resp.StatusCode, resp.Header, body)
	}
	var out struct {
		ARN          string  `json:"ARN"`
		Name         string  `json:"Name"`
		VersionID    string  `json:"VersionId"`
		SecretString *string `json:"SecretString"`
		SecretBinary *string `json:"SecretBinary"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, &Error{Code: core.FailProviderError, Status: resp.StatusCode, Msg: "the response is not the JSON GetSecretValue returns"}
	}
	v := &Value{ARN: out.ARN, Name: out.Name, VersionID: out.VersionID}
	switch {
	case out.SecretString != nil:
		v.String = []byte(*out.SecretString)
	case out.SecretBinary != nil:
		if v.Binary, err = base64.StdEncoding.DecodeString(*out.SecretBinary); err != nil {
			return nil, &Error{Code: core.FailProviderError, Status: resp.StatusCode, Msg: "SecretBinary is not base64"}
		}
	default:
		return nil, &Error{Code: core.FailNotFound, Status: resp.StatusCode, Msg: "the secret's current version holds no value"}
	}
	return v, nil
}

// classify reads an AWS JSON error: the code from X-Amzn-ErrorType, else
// the body's __type (optionally namespaced "com.amazonaws…#Code").
func classify(status int, h http.Header, body []byte) *Error {
	var e struct {
		Type     string `json:"__type"`
		Message  string `json:"message"`
		MessageU string `json:"Message"`
	}
	_ = json.Unmarshal(body, &e)
	code := errorCode(h.Get("X-Amzn-Errortype"))
	if code == "" {
		code = errorCode(e.Type)
	}
	msg := e.MessageU
	if msg == "" {
		msg = e.Message
	}
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	out := &Error{AWSCode: code, Status: status, Msg: ScrubAccounts(msg)}
	switch strings.ToLower(code) {
	case "resourcenotfoundexception":
		out.Code = core.FailNotFound
	case "invalidrequestexception":
		out.Code = core.FailUnknown
		if strings.Contains(strings.ToLower(msg), "deletion") || strings.Contains(strings.ToLower(msg), "deleted") {
			out.Code = core.FailNotFound
		}
	case "accessdeniedexception", "decryptionfailure", "notauthorized":
		out.Code = core.FailAWSDenied
	case "unrecognizedclientexception", "invalidsignatureexception", "incompletesignatureexception",
		"missingauthenticationtokenexception", "expiredtokenexception", "expiredtoken", "invalidclienttokenid",
		"signaturedoesnotmatch":
		out.Code = core.FailAWSNoCredentials
	case "throttlingexception", "toomanyrequestsexception", "limitexceededexception":
		out.Code = core.FailRateLimited
	case "internalserviceerror", "internalfailure", "serviceunavailable":
		out.Code = core.FailProviderError
	default:
		switch {
		case status == http.StatusForbidden:
			out.Code = core.FailAWSDenied
		case status == http.StatusTooManyRequests:
			out.Code = core.FailRateLimited
		case status >= 500:
			out.Code = core.FailProviderError
		default:
			out.Code = core.FailUnknown
		}
	}
	if out.Msg == "" && out.AWSCode == "" {
		out.Msg = fmt.Sprintf("HTTP %d", status)
	}
	return out
}

func errorCode(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

// transportMessage drops the request URL that net/http puts in its errors
// ("Post \"https://…\": …"): the cause is what helps.
func transportMessage(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "\": "); i >= 0 && (strings.HasPrefix(msg, "Post \"") || strings.HasPrefix(msg, "Get \"")) {
		msg = msg[i+3:]
	}
	return ScrubAccounts(msg)
}

var (
	accountRe = regexp.MustCompile(`[0-9]{12,}`)
	sessionRe = regexp.MustCompile(`(assumed-role/[\w+=,.@-]+/|federated-user/)[\w+=,.@-]+`)
)

// ScrubAccounts replaces 12-digit AWS account ids and role session names
// (with SSO the session name is the person's sign-in, often an email
// address), as the bedrock client does for its errors, which members can see.
func ScrubAccounts(s string) string {
	s = accountRe.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) == 12 {
			return "<account>"
		}
		return m
	})
	return sessionRe.ReplaceAllString(s, "${1}<session>")
}
