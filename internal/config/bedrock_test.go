package config

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
)

func TestBedrockProviders(t *testing.T) {
	dir := t.TempDir()
	file := `providers:
  - {id: aws, type: bedrock, region: us-west-2, profile: software_dev}
  - {id: gov, type: bedrock, region: us-gov-west-1, base_url: "https://vpce-1.bedrock-runtime.us-gov-west-1.vpce.amazonaws.com"}
  - {id: noregion, type: bedrock}
  - {id: badregion, type: bedrock, region: "Oregon"}
  - {id: keyed, type: bedrock, region: eu-west-1, api_key: "secret:aws_key", headers: {X-A: "secret:x"}}
  - {id: plain, type: bedrock, region: eu-west-1, base_url: "http://bedrock.internal"}
  - {id: badprofile, type: bedrock, region: eu-west-1, profile: "a b]"}
  - {id: anthropic, type: anthropic, api_key: "secret:k", region: us-east-1, profile: x}
`
	writeFile(t, filepath.Join(dir, "bothy.yaml"), file)
	m := load(t, dir)
	c, eff := m.Current(), m.Effective()
	want := []string{
		`providers.noregion: region is required for bedrock`,
		`providers.badregion: region "Oregon" is not an AWS region name`,
		`providers.keyed.api_key: bedrock uses the AWS credential chain`,
		`providers.keyed.headers.X-A: bedrock requests are signed with AWS credentials`,
		`providers.plain.base_url:`,
		`providers.badprofile.profile: invalid profile name "a b]"`,
		`providers.anthropic.region: region applies to type bedrock only`,
		`providers.anthropic.profile: profile applies to type bedrock only`,
	}
	for _, w := range want {
		if len(errorsMatching(eff, w)) != 1 {
			t.Errorf("missing error %q in %+v", w, eff.Errors)
		}
	}
	if len(eff.Errors) != len(want) {
		t.Errorf("got %d errors: %+v", len(eff.Errors), eff.Errors)
	}
	byID := map[string]Provider{}
	for _, p := range c.Providers {
		byID[p.ID] = p
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"anthropic", "aws", "badprofile", "gov", "keyed", "plain"}) {
		t.Fatalf("providers kept: %v", ids)
	}
	if p := byID["aws"]; p.Region != "us-west-2" || p.Profile != "software_dev" || p.Type != "bedrock" {
		t.Fatalf("aws provider %+v", p)
	}
	if p := byID["keyed"]; p.APIKey != "" || len(p.Headers) != 0 {
		t.Fatalf("invalid bedrock keys must fall back to their defaults: %+v", p)
	}
	if p := byID["plain"]; p.BaseURL != "" {
		t.Fatalf("an http base_url must not reach a bedrock provider: %+v", p)
	}
	if p := byID["anthropic"]; p.Region != "" || p.Profile != "" {
		t.Fatalf("bedrock-only keys must not stick to other types: %+v", p)
	}
}

func TestBedrockInSchema(t *testing.T) {
	var s struct {
		Properties map[string]struct {
			Items struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(buildSchema(Default()), &s); err != nil {
		t.Fatal(err)
	}
	props := s.Properties["providers"].Items.Properties
	if !slices.Contains(props["type"].Enum, "bedrock") {
		t.Fatalf("type enum %v", props["type"].Enum)
	}
	for _, k := range []string{"region", "profile"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema lacks providers[].%s", k)
		}
	}
}

func TestAWSNames(t *testing.T) {
	for _, r := range []string{"us-west-2", "us-gov-west-1", "eu-isoe-west-1", "cn-north-1", "ap-southeast-10"} {
		if !ValidAWSRegion(r) {
			t.Errorf("rejected region %q", r)
		}
	}
	for _, r := range []string{"", "us", "US-WEST-2", "us-west", "us-west-2.evil.com", "us-west-2/x", "us_west_2"} {
		if ValidAWSRegion(r) {
			t.Errorf("accepted region %q", r)
		}
	}
	for _, p := range []string{"default", "software_development_automation", "team.dev", "a@b=c,d+e"} {
		if !ValidAWSProfile(p) {
			t.Errorf("rejected profile %q", p)
		}
	}
	for _, p := range []string{"", " x", "a b", "x]", "[x", "-x", "a\nb"} {
		if ValidAWSProfile(p) {
			t.Errorf("accepted profile %q", p)
		}
	}
}
