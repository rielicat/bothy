package config

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const testARN = "arn:aws:secretsmanager:eu-central-1:123456789012:secret:prod/db-AbCdEf"

func TestParseAWSSecretRef(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want AWSSecretRef
		reg  string // RegionOr("us-west-2")
	}{
		{"aws-sm:prod/openai", AWSSecretRef{SecretID: "prod/openai"}, "us-west-2"},
		{"aws-sm:prod/db#password", AWSSecretRef{SecretID: "prod/db", Key: "password"}, "us-west-2"},
		{"aws-sm:prod/db#password?region=eu-west-1", AWSSecretRef{SecretID: "prod/db", Key: "password", Region: "eu-west-1"}, "eu-west-1"},
		{"aws-sm:prod/db?region=eu-west-1&profile=ops#password", AWSSecretRef{SecretID: "prod/db", Key: "password", Region: "eu-west-1", Profile: "ops"}, "eu-west-1"},
		{"aws-sm:prod/db?profile=ops", AWSSecretRef{SecretID: "prod/db", Profile: "ops"}, "us-west-2"},
		{"aws-sm:" + testARN + "#password", AWSSecretRef{SecretID: testARN, Key: "password"}, "eu-central-1"},
		{"aws-sm:" + testARN + "?region=eu-west-1", AWSSecretRef{SecretID: testARN, Region: "eu-west-1"}, "eu-west-1"},
		{"aws-sm:a+b=c.d@e-f_g", AWSSecretRef{SecretID: "a+b=c.d@e-f_g"}, "us-west-2"},
		// Secrets other AWS services manage are named "<service>!…".
		{"aws-sm:rds!cluster-b7b9a0e0-d68a#username", AWSSecretRef{SecretID: "rds!cluster-b7b9a0e0-d68a", Key: "username"}, "us-west-2"},
	} {
		got, err := ParseAWSSecretRef(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseAWSSecretRef(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			continue
		}
		if r := got.RegionOr("us-west-2"); r != tc.reg {
			t.Errorf("%q: RegionOr = %q, want %q", tc.in, r, tc.reg)
		}
		if again, err := ParseAWSSecretRef(got.String()); err != nil || again != got {
			t.Errorf("%q: String() = %q does not round-trip: %+v %v", tc.in, got.String(), again, err)
		}
	}
	for _, in := range []string{
		"", "aws-sm:", "aws-sm:#key", "secret:x", "aws-sm:prod/db#", "aws-sm:prod/db?", "aws-sm:prod/db?region=Oregon",
		"aws-sm:prod/db?zone=a", "aws-sm:prod/db?region=us-east-1&region=us-east-2", "aws-sm:prod/db?profile=a]b",
		"aws-sm:prod:db", "aws-sm:arn:aws:s3:::bucket", "aws-sm:prod/db#a#b", "aws-sm:prod/db password",
		"aws-sm:" + strings.Repeat("a", 2049), "aws-sm:x#" + strings.Repeat("k", 257),
	} {
		if r, err := ParseAWSSecretRef(in); err == nil {
			t.Errorf("ParseAWSSecretRef(%q) = %+v, want an error", in, r)
		} else if len(in) > len(AWSSecretPrefix) && strings.Contains(err.Error(), in) {
			t.Errorf("error echoes its input: %v", err)
		}
	}
}

func TestFindAWSSecretRefs(t *testing.T) {
	refs, err := FindAWSSecretRefs("Bearer aws-sm:prod/api#token, X aws-sm:" + testARN + " notaws-sm:x")
	if err != nil || len(refs) != 2 {
		t.Fatalf("refs %+v %v", refs, err)
	}
	if refs[0].Ref.SecretID != "prod/api" || refs[0].Ref.Key != "token" || refs[0].Start != 7 || refs[0].End != 28 {
		t.Errorf("first %+v", refs[0])
	}
	if refs[1].Ref.SecretID != testARN || refs[1].Ref.Key != "" {
		t.Errorf("second %+v", refs[1])
	}
	if _, err := FindAWSSecretRefs("Bearer aws-sm:?"); err == nil {
		t.Error("a broken embedded reference was accepted")
	}
	if refs, err := FindAWSSecretRefs("Bearer secret:x"); err != nil || len(refs) != 0 {
		t.Errorf("no aws refs: %+v %v", refs, err)
	}
	if !HasSecretRef("Bearer aws-sm:x") || !HasSecretRef("secret:x") || HasSecretRef("Bearer abc") {
		t.Error("HasSecretRef")
	}
}

func TestAWSSecretRefsInConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), `secrets:
  aws: {region: us-west-2, profile: ops, cache_ttl: 10m, allow_private: true}
providers:
  - {id: anthropic, type: anthropic, api_key: "aws-sm:prod/llm#anthropic"}
  - {id: router, type: openai_compat, base_url: "https://openrouter.ai/api/v1", api_key: "aws-sm:`+testARN+`", headers: {X-Org: "Org aws-sm:prod/llm#org"}}
search: {provider: brave, brave_api_key: "aws-sm:prod/search?region=eu-west-1"}
channels:
  telegram: {token: "aws-sm:prod/telegram#token"}
mcp:
  servers:
    - {name: docs, transport: http, url: "https://mcp.example.com/mcp", headers: {Authorization: "Bearer aws-sm:prod/mcp#docs"}}
releases: {github_token: "aws-sm:prod/github?profile=deploy"}
`)
	m := load(t, dir)
	c, eff := m.Current(), m.Effective()
	if len(eff.Errors) != 0 {
		t.Fatalf("errors: %+v", eff.Errors)
	}
	if a := c.Secrets.AWS; a.Region != "us-west-2" || a.Profile != "ops" || a.CacheTTL.D() != 10*time.Minute || !a.AllowPrivate {
		t.Fatalf("secrets.aws = %+v", a)
	}
	var paths []string
	for _, sf := range c.SecretFields() {
		if strings.Contains(sf.Value, AWSSecretPrefix) {
			paths = append(paths, sf.Path)
		}
	}
	want := []string{"providers.anthropic.api_key", "providers.router.api_key", "providers.router.headers.X-Org",
		"search.brave_api_key", "channels.telegram.token", "mcp.servers.docs.headers.Authorization", "releases.github_token"}
	if !slices.Equal(paths, want) {
		t.Fatalf("SecretFields with aws refs = %v\nwant %v", paths, want)
	}
	e := entry(t, eff, "secrets.aws.cache_ttl")
	if !e.Hot {
		t.Error("secrets.aws applies without a restart")
	}
}

func TestAWSSecretRefValidation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), `providers:
  - {id: a, type: anthropic, api_key: "aws-sm:prod/llm#anthropic"}
  - {id: b, type: anthropic, api_key: "aws-sm:prod/llm?region=Oregon"}
  - {id: c, type: anthropic, api_key: "aws-sm:prod/llm#k?region=eu-west-1"}
  - {id: d, type: anthropic, api_key: "aws-sm:`+testARN+`"}
  - {id: e, type: openai_compat, base_url: "https://x.example/v1", headers: {X-K: "Key aws-sm:?oops"}}
`)
	m := load(t, dir)
	c, eff := m.Current(), m.Effective()
	for _, w := range []string{
		"providers.b.api_key: invalid aws-sm reference: region= needs an AWS region name",
		"providers.e.headers.X-K: invalid aws-sm reference",
		"secrets.aws.region: is required: providers.a.api_key reads AWS Secrets Manager without ?region=",
	} {
		if len(errorsMatching(eff, w)) != 1 {
			t.Errorf("missing error %q in %+v", w, eff.Errors)
		}
	}
	if len(eff.Errors) != 3 {
		t.Errorf("got %d errors: %+v", len(eff.Errors), eff.Errors)
	}
	for _, fe := range eff.Errors {
		if strings.Contains(fe.Message, "oops") || strings.Contains(fe.Message, "Oregon") {
			t.Errorf("error echoes the value: %q", fe.Message)
		}
	}
	for _, p := range c.Providers {
		if p.ID == "a" && p.APIKey != "aws-sm:prod/llm#anthropic" {
			t.Errorf("a missing region is reported, not dropped: %q", p.APIKey)
		}
	}

	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), `secrets:
  aws: {region: "Oregon", profile: "a b", cache_ttl: 0s}
`)
	eff = load(t, dir).Effective()
	for _, w := range []string{
		`secrets.aws.region: "Oregon" is not an AWS region name`,
		`secrets.aws.profile: invalid profile name`,
		`secrets.aws.cache_ttl: must be between 1s and 24h`,
	} {
		if len(errorsMatching(eff, w)) != 1 {
			t.Errorf("missing error %q in %+v", w, eff.Errors)
		}
	}
}

func TestSetAWSSecretRefNeedsRegion(t *testing.T) {
	dir := t.TempDir()
	m := load(t, dir)
	_, err := set(t, m, map[string]string{"channels.telegram.token": `"aws-sm:prod/telegram#token"`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "secrets.aws.region: is required") {
		t.Fatalf("error %q", fe.Error())
	}
	mustSet(t, m, map[string]string{"secrets.aws.region": `"us-west-2"`})
	mustSet(t, m, map[string]string{"channels.telegram.token": `"aws-sm:prod/telegram#token"`})
	if got := m.Current().Channels.Telegram.Token; got != "aws-sm:prod/telegram#token" {
		t.Fatalf("token = %q", got)
	}
	_, err = set(t, m, map[string]string{"channels.telegram.token": `"aws-sm:prod/telegram#"`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "invalid aws-sm reference") {
		t.Fatalf("error %q", fe.Error())
	}
}

func TestAWSSecretRefInSchema(t *testing.T) {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(buildSchema(Default()), &s); err != nil {
		t.Fatal(err)
	}
	var prov struct {
		Items struct {
			Properties map[string]struct {
				Pattern              string `json:"pattern"`
				AdditionalProperties struct {
					Pattern string `json:"pattern"`
				} `json:"additionalProperties"`
			} `json:"properties"`
		} `json:"items"`
	}
	if err := json.Unmarshal(s.Properties["providers"], &prov); err != nil {
		t.Fatal(err)
	}
	exact := regexp.MustCompile(prov.Items.Properties["api_key"].Pattern)
	embed := regexp.MustCompile(prov.Items.Properties["headers"].AdditionalProperties.Pattern)
	for _, v := range []string{"", "secret:openai_api_key", "aws-sm:prod/llm#openai", "aws-sm:" + testARN + "?region=eu-west-1"} {
		if !exact.MatchString(v) {
			t.Errorf("api_key pattern rejects %q", v)
		}
	}
	for _, v := range []string{"sk-plaintext", "aws-sm:", "Bearer aws-sm:x"} {
		if exact.MatchString(v) {
			t.Errorf("api_key pattern accepts %q", v)
		}
	}
	if !embed.MatchString("Bearer aws-sm:prod/api#token") || !embed.MatchString("Bearer secret:x") || embed.MatchString("Bearer abc") {
		t.Error("header pattern")
	}
	if _, ok := s.Properties["secrets"]; !ok {
		t.Error("no secrets block in the schema")
	}
}
