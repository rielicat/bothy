package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/rielicat/bothy/internal/core"
)

const googleScopes = "openid email profile"

// GoogleAuthURL is the authorize address for r. prompt=select_account lets
// someone signed in to several Google accounts pick their work one.
func GoogleAuthURL(ep Endpoints, r AuthRequest) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {r.ClientID},
		"redirect_uri":          {r.RedirectURI},
		"scope":                 {googleScopes},
		"state":                 {r.State},
		"nonce":                 {r.Nonce},
		"code_challenge":        {r.Challenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
		"access_type":           {"online"},
	}
	if r.DomainHint != "" {
		q.Set("hd", r.DomainHint)
	}
	return ep.AuthURL + "?" + q.Encode()
}

type tokenAnswer struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	Error       string `json:"error"`
	Desc        string `json:"error_description"`
}

// googleToken posts the code to Google's token endpoint. Only the id token
// is used: the access token is dropped unread.
func (c *Client) googleToken(ctx context.Context, ep Endpoints, form url.Values) (string, error) {
	hc, err := c.client("signin:google", ep)
	if err != nil {
		return "", err
	}
	status, body, err := postForm(ctx, hc, "google token", ep.TokenURL, form)
	if err != nil {
		return "", err
	}
	var t tokenAnswer
	jerr := json.Unmarshal(body, &t)
	if status != 200 {
		switch t.Error {
		case "invalid_client", "unauthorized_client":
			return "", &ProviderError{Op: "google token", Status: status, Code: core.FailKeyRejected, Desc: clip(t.Error)}
		case "invalid_grant":
			return "", ErrRejected
		}
		return "", &ProviderError{Op: "google token", Status: status, Desc: clip(t.Error)}
	}
	if jerr != nil {
		return "", &ProviderError{Op: "google token", Status: status, Desc: "unreadable answer"}
	}
	return t.IDToken, nil
}

// Google trades x.Code for the person's identity and checks it against r.
// Only a verified email ever passes.
func (c *Client) Google(ctx context.Context, ep Endpoints, x Exchange, r Rules) (Identity, error) {
	idToken, err := c.googleToken(ctx, ep, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {x.Code},
		"redirect_uri":  {x.RedirectURI},
		"client_id":     {x.ClientID},
		"client_secret": {x.Secret.Reveal()},
		"code_verifier": {x.Verifier},
	})
	if err != nil {
		return Identity{}, err
	}
	cl, err := decodeIDToken(idToken)
	if err != nil {
		return Identity{}, err
	}
	if err := cl.check(x.ClientID, x.Nonce, c.now()); err != nil {
		return Identity{}, err
	}
	id := Identity{Subject: cl.Sub, Email: cl.Email, Name: cl.Name}
	return id, cl.allowed(r.Allow)
}

// CheckGoogle posts a made-up code with the client's credentials: Google
// answers invalid_grant only once it has accepted the client.
func (c *Client) CheckGoogle(ctx context.Context, ep Endpoints, clientID, redirect string, secret core.Secret) error {
	verifier, _ := NewPKCE()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"bothy-check"},
		"client_id":     {clientID},
		"client_secret": {secret.Reveal()},
		"code_verifier": {verifier},
	}
	if redirect != "" {
		form.Set("redirect_uri", redirect)
	}
	_, err := c.googleToken(ctx, ep, form)
	if errors.Is(err, ErrRejected) {
		return nil
	}
	return err
}
