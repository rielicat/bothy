package awssm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rielicat/bothy/internal/core"
)

// toServer sends every request to srv, keeping the Host header the client
// signed, and refuses hosts other than allow like an egress system client.
type toServer struct {
	srv   *httptest.Server
	allow string
}

func (t toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host+":443" != t.allow {
		return nil, errors.New("egress: " + req.URL.Host + " is not a configured host")
	}
	u, _ := url.Parse(t.srv.URL)
	r := req.Clone(req.Context())
	r.Host = req.URL.Host
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

type recorded struct {
	method, path, host, auth, target, ctype, token, body string
}

func fake(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recorded{r.Method, r.URL.Path, r.Host, r.Header.Get("Authorization"), r.Header.Get("X-Amz-Target"),
			r.Header.Get("Content-Type"), r.Header.Get("X-Amz-Security-Token"), string(b)})
		mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recorded { mu.Lock(); defer mu.Unlock(); return append([]recorded(nil), got...) }
}

func staticCreds(session string) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: "secret-key-never-sent", SessionToken: session}, nil
	})
}

func client(t *testing.T, srv *httptest.Server, region string, creds aws.CredentialsProvider) *Client {
	t.Helper()
	c, err := New(Options{Region: region, Credentials: creds,
		HTTP: &http.Client{Transport: toServer{srv: srv, allow: Host(region)}},
		Now:  func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func reply(status int, body string, hdr ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func TestGetSecretValueString(t *testing.T) {
	srv, reqs := fake(t, reply(200, `{"ARN":"arn:aws:secretsmanager:us-west-2:123456789012:secret:prod/db-AbCdEf","Name":"prod/db","VersionId":"v1","SecretString":"{\"password\":\"hunter2-long\"}","VersionStages":["AWSCURRENT"]}`))
	v, err := client(t, srv, "us-west-2", staticCreds("session-token")).GetSecretValue(context.Background(), "prod/db")
	if err != nil {
		t.Fatal(err)
	}
	if string(v.String) != `{"password":"hunter2-long"}` || v.Binary != nil || v.VersionID != "v1" || v.Name != "prod/db" {
		t.Fatalf("value %+v", v)
	}
	r := reqs()
	if len(r) != 1 {
		t.Fatalf("%d requests", len(r))
	}
	got := r[0]
	if got.method != http.MethodPost || got.path != "/" || got.host != "secretsmanager.us-west-2.amazonaws.com" ||
		got.target != "secretsmanager.GetSecretValue" || got.ctype != "application/x-amz-json-1.1" || got.body != `{"SecretId":"prod/db"}` {
		t.Errorf("request %+v", got)
	}
	if !strings.HasPrefix(got.auth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/20261014/us-west-2/secretsmanager/aws4_request, SignedHeaders=") ||
		!strings.Contains(got.auth, "x-amz-target") || got.token != "session-token" {
		t.Errorf("signature %q token %q", got.auth, got.token)
	}
	if strings.Contains(got.auth, "secret-key-never-sent") {
		t.Error("the secret access key was sent")
	}
}

func TestGetSecretValueBinary(t *testing.T) {
	raw := []byte{0, 1, 2, 0xff, 'k', 'e', 'y'}
	srv, _ := fake(t, reply(200, `{"Name":"bin","VersionId":"v9","SecretBinary":"`+base64.StdEncoding.EncodeToString(raw)+`"}`))
	v, err := client(t, srv, "eu-west-1", staticCreds("")).GetSecretValue(context.Background(), "bin")
	if err != nil || string(v.Binary) != string(raw) || v.String != nil {
		t.Fatalf("value %+v %v", v, err)
	}
}

func TestGetSecretValueErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		h      func(http.ResponseWriter, *http.Request)
		code   core.FailureCode
		awsErr string
	}{
		{"not found", reply(400, `{"__type":"ResourceNotFoundException","Message":"Secrets Manager can't find the specified secret."}`), core.FailNotFound, "ResourceNotFoundException"},
		{"deleted", reply(400, `{"__type":"InvalidRequestException","Message":"You can't perform this operation on the secret because it was marked for deletion."}`), core.FailNotFound, "InvalidRequestException"},
		{"denied", reply(400, `{"__type":"AccessDeniedException","Message":"User: arn:aws:sts::123456789012:assumed-role/Dev/jane@example.com is not authorized to perform: secretsmanager:GetSecretValue"}`), core.FailAWSDenied, "AccessDeniedException"},
		{"kms", reply(400, `{"__type":"DecryptionFailure","Message":"Access to KMS is not allowed"}`), core.FailAWSDenied, "DecryptionFailure"},
		{"expired", reply(400, `{"__type":"com.amazon.coral.service#ExpiredTokenException","message":"The security token included in the request is expired"}`), core.FailAWSNoCredentials, "ExpiredTokenException"},
		{"header code", reply(400, `{}`, "X-Amzn-ErrorType", "UnrecognizedClientException:http://internal.amazon.com/coral/"), core.FailAWSNoCredentials, "UnrecognizedClientException"},
		{"throttled", reply(400, `{"__type":"ThrottlingException","Message":"Rate exceeded"}`), core.FailRateLimited, "ThrottlingException"},
		{"internal", reply(500, `{"__type":"InternalServiceError"}`), core.FailProviderError, "InternalServiceError"},
		{"bare 503", reply(503, `oops`), core.FailProviderError, ""},
		{"bare 403", reply(403, ``), core.FailAWSDenied, ""},
		{"redirect not followed", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				t.Errorf("redirect followed to %s", r.URL.Path)
			}
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}, core.FailUnknown, ""},
		{"no value", reply(200, `{"Name":"x","VersionId":"v1"}`), core.FailNotFound, ""},
		{"not json", reply(200, `<html>`), core.FailProviderError, ""},
		{"too large", reply(200, `{"SecretString":"`+strings.Repeat("a", maxBody)+`"}`), core.FailProviderError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fake(t, tc.h)
			_, err := client(t, srv, "us-west-2", staticCreds("")).GetSecretValue(context.Background(), "prod/db")
			var e *Error
			if !errors.As(err, &e) || e.Code != tc.code || e.AWSCode != tc.awsErr {
				t.Fatalf("err = %#v, want code %s aws %q", err, tc.code, tc.awsErr)
			}
			if core.CodeOf(err) != tc.code {
				t.Errorf("CodeOf = %s", core.CodeOf(err))
			}
			if strings.Contains(err.Error(), "123456789012") || strings.Contains(err.Error(), "jane@example.com") {
				t.Errorf("error keeps the account or session: %v", err)
			}
		})
	}
}

func TestCredentialAndTransportFailures(t *testing.T) {
	srv, reqs := fake(t, reply(200, `{}`))
	noCreds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New("failed to refresh cached credentials, the SSO session for account 123456789012 has expired")
	})
	_, err := client(t, srv, "us-west-2", noCreds).GetSecretValue(context.Background(), "x")
	if core.CodeOf(err) != core.FailAWSNoCredentials || strings.Contains(err.Error(), "123456789012") || len(reqs()) != 0 {
		t.Fatalf("credentials failure: %v (requests %d)", err, len(reqs()))
	}
	// AWS's sign-in service out of reach is the network, not missing access;
	// the metadata address not answering off AWS still is no credentials.
	for endpoint, want := range map[string]core.FailureCode{
		"https://oidc.us-west-2.amazonaws.com/token": core.FailUnreachable,
		"http://169.254.169.254/latest/api/token":    core.FailAWSNoCredentials,
	} {
		offline := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{}, fmt.Errorf("failed to refresh cached credentials, %w",
				&url.Error{Op: "Post", URL: endpoint, Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}})
		})
		_, err := client(t, srv, "us-west-2", offline).GetSecretValue(context.Background(), "x")
		var ae *Error
		if core.CodeOf(err) != want || !errors.As(err, &ae) || !ae.Credentials || len(reqs()) != 0 {
			t.Errorf("%s out of reach: %q %v", endpoint, core.CodeOf(err), err)
		}
	}

	c, err := New(Options{Region: "us-west-2", Credentials: staticCreds(""),
		HTTP: &http.Client{Transport: toServer{srv: srv, allow: Host("eu-west-1")}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetSecretValue(context.Background(), "x")
	if core.CodeOf(err) != core.FailUnreachable || strings.Contains(err.Error(), "https://") {
		t.Fatalf("refused host: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client(t, srv, "us-west-2", noCreds).GetSecretValue(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestNewRefusesUnsafeClients(t *testing.T) {
	for _, o := range []Options{
		{Credentials: staticCreds(""), HTTP: &http.Client{Transport: http.DefaultTransport}},
		{Region: "us-west-2", HTTP: &http.Client{Transport: http.DefaultTransport}},
		{Region: "us-west-2", Credentials: staticCreds("")},
		{Region: "us-west-2", Credentials: staticCreds(""), HTTP: &http.Client{}},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("New(%+v) succeeded", o)
		}
	}
}

func TestHost(t *testing.T) {
	if Host("us-west-2") != "secretsmanager.us-west-2.amazonaws.com:443" || Host("cn-north-1") != "secretsmanager.cn-north-1.amazonaws.com.cn:443" {
		t.Error(Host("us-west-2"), Host("cn-north-1"))
	}
	if got := ScrubAccounts("arn:aws:sts::123456789012:assumed-role/Dev/jane@example.com"); got != "arn:aws:sts::<account>:assumed-role/Dev/<session>" {
		t.Error(got)
	}
}
