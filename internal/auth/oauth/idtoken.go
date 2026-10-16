package oauth

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxIDToken = 16 << 10
	expLeeway  = 60 * time.Second
	iatLeeway  = 300 * time.Second
)

var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

type googleClaims struct {
	Iss           string          `json:"iss"`
	Aud           audience        `json:"aud"`
	Azp           string          `json:"azp"`
	Exp           float64         `json:"exp"`
	Iat           float64         `json:"iat"`
	Nonce         string          `json:"nonce"`
	Sub           string          `json:"sub"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	HD            string          `json:"hd"`
	Name          string          `json:"name"`
}

func badToken(desc string) error {
	return &ProviderError{Op: "google id token", Desc: desc}
}

// decodeIDToken reads the claims of an id token without checking its
// signature. That is sound only for a token received straight from Google's
// token endpoint over TLS by a client that authenticated with its secret,
// which is the one place it is called from: OIDC Core 1.0 §3.1.3.7 (6) lets
// TLS server validation stand in for the signature check there.
func decodeIDToken(tok string) (*googleClaims, error) {
	if tok == "" {
		return nil, badToken("no id_token in the answer")
	}
	if len(tok) > maxIDToken {
		return nil, badToken("id_token too large")
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, badToken("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, badToken("malformed id_token payload")
	}
	var cl googleClaims
	if err := json.Unmarshal(payload, &cl); err != nil {
		return nil, badToken("unreadable id_token claims")
	}
	return &cl, nil
}

// check applies the OIDC claim checks. Failing one means Google's answer
// can't be trusted, which is not the person's doing, so it is a
// ProviderError rather than ErrNotAllowed.
func (cl *googleClaims) check(clientID, nonce string, now time.Time) error {
	switch {
	case !slices.Contains(googleIssuers, cl.Iss):
		return badToken("unexpected issuer")
	case !slices.Contains(cl.Aud, clientID):
		return badToken("id_token is for another client")
	case cl.Azp != "" && cl.Azp != clientID:
		return badToken("id_token authorized another client")
	case cl.Exp == 0 || time.Unix(int64(cl.Exp), 0).Before(now.Add(-expLeeway)):
		return badToken("id_token expired")
	case time.Unix(int64(cl.Iat), 0).After(now.Add(iatLeeway)):
		return badToken("id_token issued in the future")
	case nonce == "" || subtle.ConstantTimeCompare([]byte(cl.Nonce), []byte(nonce)) != 1:
		return badToken("nonce mismatch")
	case cl.Sub == "" || utf8.RuneCountInString(cl.Sub) > 255:
		return badToken("missing or overlong subject")
	case cl.Email == "" || len(cl.Email) > 320:
		return badToken("missing or overlong email claim")
	}
	return nil
}

// allowed applies the sign-in list. A domain entry matches only a Google
// Workspace account of that domain, through hd: anyone can create a
// consumer Google account for a work address, and such an account has no
// hd, so an admin lists that person's address instead. hd is the Workspace
// organization's primary domain, so people whose address is on one of its
// secondary domains still match.
func (cl *googleClaims) allowed(allow []string) error {
	if v := bytes.TrimSpace(cl.EmailVerified); !bytes.Equal(v, []byte("true")) && !bytes.Equal(v, []byte(`"true"`)) {
		return fmt.Errorf("%w: Google has not verified this email address", ErrNotAllowed)
	}
	if listedAddress(allow, cl.Email) || listedDomain(allow, cl.HD) {
		return nil
	}
	return fmt.Errorf("%w: neither the address nor the Workspace domain is on the list", ErrNotAllowed)
}
