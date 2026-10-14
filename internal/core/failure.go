package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"syscall"
)

// FailureCode names why a provider, channel or GitHub check, or an external
// secret reference, failed, so the web UI picks its sentence from a code
// instead of parsing messages (v0.2 UI §5.6.4). The raw message always
// travels next to it.
type FailureCode string

const (
	FailKeyRejected      FailureCode = "key_rejected"       // the key or token was not accepted
	FailNoModelAccess    FailureCode = "no_model_access"    // the key works but cannot use this model
	FailOutOfCredit      FailureCode = "out_of_credit"      // the account has no credit or quota left
	FailRateLimited      FailureCode = "rate_limited"       // the service is limiting requests
	FailProviderError    FailureCode = "provider_error"     // the service failed on its side (5xx, overloaded)
	FailTimeout          FailureCode = "timeout"            // no answer in time
	FailUnreachable      FailureCode = "unreachable"        // connection refused, unknown host, no route
	FailPrivateAddress   FailureCode = "private_address"    // egress refused a private address without allow_private
	FailNoModels         FailureCode = "no_models"          // the server answers but lists no models
	FailTLS              FailureCode = "tls"                // certificate or TLS handshake problem
	FailNotAModelServer  FailureCode = "not_a_model_server" // something answered that does not speak the model API
	FailAWSNoCredentials FailureCode = "aws_no_credentials" // the AWS credential chain resolved nothing
	FailAWSDenied        FailureCode = "aws_denied"         // AWS refused this identity
	FailAWSNoModels      FailureCode = "aws_no_models"      // no Claude or Nova model is enabled in the region
	FailNotFound         FailureCode = "not_found"          // a cloud secret store has no such secret
	FailJSONKeyMissing   FailureCode = "json_key_missing"   // the secret is a JSON object without the referenced key
	FailNotJSON          FailureCode = "not_json"           // a JSON key was referenced but the secret is not a JSON object
	FailUnknown          FailureCode = "unknown"
)

// FailureCoder is an error that knows its FailureCode.
type FailureCoder interface{ FailureCode() FailureCode }

// CodeOf returns the code err carries (FailureCoder), else its
// TransportFailure, else "".
func CodeOf(err error) FailureCode {
	var fc FailureCoder
	if errors.As(err, &fc) {
		if c := fc.FailureCode(); c != "" {
			return c
		}
	}
	return TransportFailure(err)
}

// privateRanges are the ranges egress admits only with allow_private; a
// refusal for one of them is fixed by allowing that exact address.
var privateRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.168.0.0/16", "::1/128", "fc00::/7", "fe80::/10"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// blockedRangeRe reads the range out of egress's refusal ("destination
// address blocked: … (address in blocked range 127.0.0.0/8)").
var blockedRangeRe = regexp.MustCompile(`blocked range ([0-9a-fA-F.:]+/[0-9]+)`)

// TransportFailure classifies a failure to reach a service at all: timeout,
// tls, private_address or unreachable. It returns "" for anything else (an
// HTTP answer, a malformed reply), which the caller classifies itself.
func TransportFailure(err error) FailureCode {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return FailTimeout
	case strings.Contains(msg, "destination address blocked"):
		if m := blockedRangeRe.FindStringSubmatch(msg); m != nil {
			if p, perr := netip.ParsePrefix(m[1]); perr == nil {
				for _, r := range privateRanges {
					if r == p.Masked() {
						return FailPrivateAddress
					}
				}
			}
		}
		return FailUnreachable
	case isTLS(err, msg):
		return FailTLS
	case strings.Contains(msg, "destination not allowed"), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.ECONNRESET):
		return FailUnreachable
	}
	var de *net.DNSError
	var oe *net.OpError
	if errors.As(err, &de) || errors.As(err, &oe) {
		return FailUnreachable
	}
	// Messages carried as text only (a redacted provider error) still name
	// the condition.
	for _, s := range []string{"i/o timeout", "deadline exceeded", "Client.Timeout"} {
		if strings.Contains(msg, s) {
			return FailTimeout
		}
	}
	for _, s := range []string{"connection refused", "no such host", "no route to host", "network is unreachable",
		"connection reset", "server misbehaving"} {
		if strings.Contains(msg, s) {
			return FailUnreachable
		}
	}
	return ""
}

func isTLS(err error, msg string) bool {
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var he x509.HostnameError
	var cv *tls.CertificateVerificationError
	var rh tls.RecordHeaderError
	if errors.As(err, &ua) || errors.As(err, &ci) || errors.As(err, &he) || errors.As(err, &cv) || errors.As(err, &rh) {
		return true
	}
	return strings.Contains(msg, "x509: ") || strings.Contains(msg, "tls: ")
}
