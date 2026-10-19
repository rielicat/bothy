package core

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"testing"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type codedErr struct{ code FailureCode }

func (e codedErr) Error() string            { return "coded" }
func (e codedErr) FailureCode() FailureCode { return e.code }

func TestTransportFailure(t *testing.T) {
	urlErr := func(err error) error { return &url.Error{Op: "Post", URL: "http://x/v1", Err: err} }
	for _, tc := range []struct {
		err  error
		want FailureCode
	}{
		{nil, ""},
		{fmt.Errorf("probe: %w", context.DeadlineExceeded), FailTimeout},
		{urlErr(timeoutErr{}), FailTimeout},
		{urlErr(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), FailUnreachable},
		{urlErr(&net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}), FailUnreachable},
		{urlErr(x509.UnknownAuthorityError{}), FailTLS},
		{errors.New("destination address blocked: 127.0.0.1 resolves to 127.0.0.1 (address in blocked range 127.0.0.0/8)"), FailPrivateAddress},
		{errors.New("destination address blocked: host.docker.internal resolves to 172.17.0.1 (address in blocked range 172.16.0.0/12)"), FailPrivateAddress},
		{errors.New("destination address blocked: x resolves to 169.254.169.254 (cloud metadata address)"), FailUnreachable},
		{errors.New("destination address blocked: x resolves to 224.0.0.1 (address in blocked range 224.0.0.0/4)"), FailUnreachable},
		{errors.New("egress: destination not allowed: example.com:443"), FailUnreachable},
		{errors.New("provider: network: dial tcp 10.0.0.9:11434: connect: connection refused"), FailUnreachable},
		{errors.New("HTTP 401 Unauthorized"), ""},
	} {
		if got := TransportFailure(tc.err); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestCodeOf(t *testing.T) {
	if got := CodeOf(fmt.Errorf("slack: %w", codedErr{FailKeyRejected})); got != FailKeyRejected {
		t.Fatalf("coded: %q", got)
	}
	if got := CodeOf(fmt.Errorf("x: %w", codedErr{""})); got != "" {
		t.Fatalf("empty code: %q", got)
	}
	if got := CodeOf(context.DeadlineExceeded); got != FailTimeout {
		t.Fatalf("transport: %q", got)
	}
}

// A named credential service out of reach is the network; the metadata
// address not answering is no credentials there at all.
func TestHostFailure(t *testing.T) {
	dns := &net.DNSError{Err: "no such host", Name: "oidc.us-east-1.amazonaws.com", IsNotFound: true}
	for _, tc := range []struct {
		err  error
		want FailureCode
	}{
		{fmt.Errorf("failed to refresh cached credentials, %w", &url.Error{Op: "Post", URL: "https://oidc.us-east-1.amazonaws.com/token", Err: dns}), FailUnreachable},
		{&url.Error{Op: "Post", URL: "https://sts.amazonaws.com/", Err: timeoutErr{}}, FailTimeout},
		{&url.Error{Op: "Put", URL: "http://169.254.169.254/latest/api/token", Err: timeoutErr{}}, ""},
		{&url.Error{Op: "Get", URL: "http://[fd00:ec2::254]/latest/meta-data/", Err: syscall.EHOSTUNREACH}, ""},
		{errors.New("dial tcp: lookup sts.amazonaws.com: no such host"), ""},
		{&url.Error{Op: "Post", URL: "https://sts.amazonaws.com/", Err: errors.New("HTTP 403")}, ""},
	} {
		if got := HostFailure(tc.err); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.err, got, tc.want)
		}
	}
}
