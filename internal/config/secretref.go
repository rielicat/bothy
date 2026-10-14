package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// AWSSecretPrefix starts a reference to a secret in AWS Secrets Manager.
const AWSSecretPrefix = "aws-sm:"

const (
	maxAWSSecretID  = 2048 // the longest ARN Secrets Manager accepts
	maxAWSSecretKey = 256
)

// awsRefPattern is the shape of an aws-sm reference for the JSON Schema;
// ParseAWSSecretRef also checks the ARN, the query and the lengths.
const awsRefPattern = `aws-sm:[A-Za-z0-9/_+=.@!:-]+(?:#[A-Za-z0-9/_+=.@-]+)?(?:\?[A-Za-z0-9_+=.@,&-]+)?(?:#[A-Za-z0-9/_+=.@-]+)?`

// awsSecretARNRe is a Secrets Manager secret ARN in a partition whose
// endpoints Bothy builds (awssm.DNSSuffix); the region it names is where
// the secret must be read.
var awsSecretARNRe = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):secretsmanager:([a-z]{2}(?:-[a-z]+)+-[0-9]{1,2}):[0-9]{12}:secret:[A-Za-z0-9/_+=.@!-]+$`)

// ValidAWSSecretsRegion reports whether aws-sm references can be read in
// region: a region name outside the isolated partitions.
func ValidAWSSecretsRegion(region string) bool {
	return ValidAWSRegion(region) && awsPartition(region) != ""
}

// awsPartition is the partition of a region; "" for the isolated ones,
// whose endpoints are not under amazonaws.com.
func awsPartition(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "aws-cn"
	case strings.HasPrefix(region, "us-gov-"):
		return "aws-us-gov"
	case strings.Contains(region, "-iso"):
		return ""
	}
	return "aws"
}

// AWSSecretRef is a parsed "aws-sm:<secret name or ARN>[#<json key>][?region=<region>&profile=<profile>]".
type AWSSecretRef struct {
	SecretID string // a secret name or its full ARN
	Key      string // a top-level key of a JSON secret; "" = the whole value
	Region   string // ?region=; "" = the ARN's region, else secrets.aws.region
	Profile  string // ?profile=; "" = secrets.aws.profile
}

// RegionOr is the region the secret is read in: the reference's own, else
// its ARN's, else def.
func (r AWSSecretRef) RegionOr(def string) string {
	if r.Region != "" {
		return r.Region
	}
	if m := awsSecretARNRe.FindStringSubmatch(r.SecretID); m != nil {
		return m[2]
	}
	return def
}

// String is the canonical form of the reference.
func (r AWSSecretRef) String() string {
	s := AWSSecretPrefix + r.SecretID
	if r.Key != "" {
		s += "#" + r.Key
	}
	var q []string
	if r.Region != "" {
		q = append(q, "region="+r.Region)
	}
	if r.Profile != "" {
		q = append(q, "profile="+r.Profile)
	}
	if len(q) > 0 {
		s += "?" + strings.Join(q, "&")
	}
	return s
}

// AWSSecretRefAt is an aws-sm reference found inside a longer value.
type AWSSecretRefAt struct {
	Start, End int // byte offsets of the reference in the value
	Ref        AWSSecretRef
}

// awsRefSyntax is appended to every parse error; errors never quote the
// value, which may be a mistyped plaintext secret.
const awsRefSyntax = "write aws-sm:<secret name or ARN>[#<json key>][?region=<region>&profile=<profile>]"

// ParseAWSSecretRef parses s as exactly one aws-sm reference.
func ParseAWSSecretRef(s string) (AWSSecretRef, error) {
	if !strings.HasPrefix(s, AWSSecretPrefix) {
		return AWSSecretRef{}, errors.New("not an aws-sm reference; " + awsRefSyntax)
	}
	n, r, err := scanAWSSecretRef(s)
	if err != nil {
		return AWSSecretRef{}, err
	}
	if n != len(s) {
		return AWSSecretRef{}, errors.New("unexpected characters after the aws-sm reference; " + awsRefSyntax)
	}
	return r, nil
}

// FindAWSSecretRefs returns the aws-sm references inside s (a header value
// may embed one: "Bearer aws-sm:api#token"). A marker preceded by a letter,
// digit, '_', '.' or '-' is part of a longer word, as with secret:<name>.
func FindAWSSecretRefs(s string) ([]AWSSecretRefAt, error) {
	var out []AWSSecretRefAt
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], AWSSecretPrefix)
		if j < 0 {
			break
		}
		start := i + j
		if start > 0 && refWordByte(s[start-1]) || followsSecretMarker(s, start) {
			i = start + len(AWSSecretPrefix)
			continue
		}
		n, r, err := scanAWSSecretRef(s[start:])
		if err != nil {
			return nil, err
		}
		out = append(out, AWSSecretRefAt{Start: start, End: start + n, Ref: r})
		i = start + n
	}
	return out, nil
}

// HasSecretRef reports whether s holds a secret:<name> or aws-sm reference.
func HasSecretRef(s string) bool {
	return strings.Contains(s, "secret:") || strings.Contains(s, AWSSecretPrefix)
}

// followsSecretMarker reports whether the aws-sm marker at start is the
// name of a secret:<name> reference ("secret:aws-sm" then text), which the
// vault resolves; reading it as an aws-sm reference as well would overlap.
func followsSecretMarker(s string, start int) bool {
	const m = "secret:"
	return strings.HasSuffix(s[:start], m) && (start == len(m) || !refWordByte(s[start-len(m)-1]))
}

func refWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

func awsIDByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("/_+=.@!:-", c) >= 0
}

func awsKeyByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("/_+=.@-", c) >= 0
}

func awsQueryByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_+=.@,&-", c) >= 0
}

func scanWhile(s string, i int, ok func(byte) bool) int {
	for i < len(s) && ok(s[i]) {
		i++
	}
	return i
}

// scanAWSSecretRef reads the reference at the start of s (which begins
// with the prefix) and returns its length. The JSON key and the query may
// come in either order.
func scanAWSSecretRef(s string) (int, AWSSecretRef, error) {
	var r AWSSecretRef
	i := len(AWSSecretPrefix)
	end := scanWhile(s, i, awsIDByte)
	r.SecretID = s[i:end]
	switch {
	case r.SecretID == "":
		return 0, r, errors.New("an aws-sm reference needs a secret name or ARN; " + awsRefSyntax)
	case len(r.SecretID) > maxAWSSecretID:
		return 0, r, fmt.Errorf("the secret name or ARN is longer than %d characters", maxAWSSecretID)
	case strings.HasPrefix(r.SecretID, "arn:"):
		m := awsSecretARNRe.FindStringSubmatch(r.SecretID)
		if m == nil {
			return 0, r, errors.New("the ARN is not a Secrets Manager secret ARN (arn:aws:secretsmanager:<region>:<account>:secret:<name>)")
		}
		if awsPartition(m[2]) != m[1] {
			return 0, r, errors.New("the ARN's partition does not match its region: arn:aws-cn for cn-* regions, arn:aws-us-gov for us-gov-*, arn:aws elsewhere")
		}
	case strings.Contains(r.SecretID, ":"):
		return 0, r, errors.New("a secret name cannot contain ':'; use the name or the full ARN")
	}
	i = end
	var haveKey, haveQuery bool
	for i < len(s) {
		switch {
		case s[i] == '#' && !haveKey:
			end = scanWhile(s, i+1, awsKeyByte)
			r.Key, haveKey = s[i+1:end], true
			if r.Key == "" || len(r.Key) > maxAWSSecretKey {
				return 0, r, fmt.Errorf("the JSON key after '#' must be 1 to %d characters of letters, digits and /_+=.@-", maxAWSSecretKey)
			}
		case s[i] == '?' && !haveQuery:
			end = scanWhile(s, i+1, awsQueryByte)
			if err := parseAWSQuery(s[i+1:end], &r); err != nil {
				return 0, r, err
			}
			haveQuery = true
		default:
			return i, r, nil
		}
		i = end
	}
	return i, r, nil
}

func parseAWSQuery(q string, r *AWSSecretRef) error {
	if q == "" {
		return errors.New("an empty query after '?'; use ?region=<region> and/or &profile=<profile>")
	}
	var seen []string
	for _, kv := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(kv, "=")
		if slices.Contains(seen, k) {
			return fmt.Errorf("%s is given twice", k)
		}
		seen = append(seen, k)
		switch k {
		case "region":
			if !ValidAWSSecretsRegion(v) {
				return errors.New("region= needs an AWS region name such as us-west-2 (not one of an isolated partition)")
			}
			r.Region = v
		case "profile":
			if !ValidAWSProfile(v) {
				return errors.New("profile= needs a shared-config profile name")
			}
			r.Profile = v
		default:
			return errors.New("an aws-sm reference takes only region= and profile= after '?'")
		}
	}
	return nil
}

// SecretField is one non-empty secret field of a configuration.
type SecretField struct {
	Path  string // "providers.openai.api_key", "mcp.servers.docs.headers.Authorization"
	Value string // the reference as written; a header value may embed it
}

// SecretFields lists the non-empty secret fields of c in schema order (map
// keys sorted), for reports that need every reference the configuration
// makes.
func (c *Config) SecretFields() []SecretField {
	if c == nil {
		return nil
	}
	var out []SecretField
	walkSecrets(rootField(), reflect.ValueOf(c).Elem(), nil, func(segs []string, v string) {
		out = append(out, SecretField{Path: pathKey(segs), Value: v})
	})
	return out
}

func walkSecrets(f *field, v reflect.Value, segs []string, fn func([]string, string)) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	switch f.kind {
	case kStruct:
		t := v.Type()
		for i := range t.NumField() {
			sf := t.Field(i)
			key, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
			if c := f.byName[key]; c != nil && sf.IsExported() {
				walkSecrets(c, v.Field(i), appendSeg(segs, key), fn)
			}
		}
	case kKeyedList:
		for i := range v.Len() {
			item := v.Index(i)
			id := item.FieldByNameFunc(func(n string) bool { return strings.EqualFold(n, f.key) })
			if !id.IsValid() || id.Kind() != reflect.String {
				continue
			}
			walkSecrets(f.elem, item, appendSeg(segs, id.String()), fn)
		}
	case kMap:
		if f.elem.kind != kScalar || !f.elem.secret || v.Type().Key().Kind() != reflect.String {
			return
		}
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		for _, k := range keys {
			if s := v.MapIndex(k); s.Kind() == reflect.String && s.String() != "" {
				fn(appendSeg(segs, k.String()), s.String())
			}
		}
	case kScalar:
		if f.secret && v.Kind() == reflect.String && v.String() != "" {
			fn(segs, v.String())
		}
	}
}
