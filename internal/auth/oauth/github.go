package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	githubScopes     = "read:org user:email"
	githubAPIVersion = "2022-11-28"
	revokeTimeout    = 5 * time.Second
)

// GitHubAuthURL is the authorize address for r. allow_signup=false keeps
// the consent page from offering to create a GitHub account.
func GitHubAuthURL(ep Endpoints, r AuthRequest) string {
	q := url.Values{
		"client_id":             {r.ClientID},
		"redirect_uri":          {r.RedirectURI},
		"scope":                 {githubScopes},
		"state":                 {r.State},
		"code_challenge":        {r.Challenge},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
		"prompt":                {"select_account"},
	}
	return ep.AuthURL + "?" + q.Encode()
}

// githubToken trades a code at GitHub's token endpoint, which answers 200
// with an error field when it refuses.
func githubToken(ctx context.Context, hc *http.Client, ep Endpoints, form url.Values) (string, error) {
	status, body, err := postForm(ctx, hc, "github token", ep.TokenURL, form)
	if err != nil {
		return "", err
	}
	var t tokenAnswer
	jerr := json.Unmarshal(body, &t)
	switch t.Error {
	case "":
	case "incorrect_client_credentials":
		return "", &ProviderError{Op: "github token", Status: status, Code: core.FailKeyRejected, Desc: t.Error}
	case "bad_verification_code":
		return "", ErrRejected
	default:
		return "", &ProviderError{Op: "github token", Status: status, Desc: clip(t.Error)}
	}
	if status != 200 || jerr != nil || t.AccessToken == "" {
		return "", &ProviderError{Op: "github token", Status: status, Desc: "no access token in the answer"}
	}
	return t.AccessToken, nil
}

type githubAPI struct {
	hc    *http.Client
	base  string
	token string
}

func (g githubAPI) get(ctx context.Context, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, nil)
	if err != nil {
		return 0, &ProviderError{Op: "github api", Err: err}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	status, body, err := do(ctx, g.hc, "github api", req, maxAPI)
	if err != nil {
		return status, err
	}
	if status == 200 && out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return status, &ProviderError{Op: "github api", Status: status, Desc: "unreadable answer"}
		}
	}
	return status, nil
}

// GitHub trades x.Code for the person's identity and checks it against r
// with their own token: a verified address listed exactly, or an active
// membership of a listed organization; domain entries never match here. The
// token is revoked as soon as the checks are done, whatever their outcome.
func (c *Client) GitHub(ctx context.Context, ep Endpoints, x Exchange, r Rules) (Identity, error) {
	hc, err := c.client("signin:github", ep)
	if err != nil {
		return Identity{}, err
	}
	tok, err := githubToken(ctx, hc, ep, url.Values{
		"client_id":     {x.ClientID},
		"client_secret": {x.Secret.Reveal()},
		"code":          {x.Code},
		"redirect_uri":  {x.RedirectURI},
		"code_verifier": {x.Verifier},
	})
	if err != nil {
		return Identity{}, err
	}
	defer c.revoke(ctx, hc, ep, x.ClientID, x.Secret, tok)
	api := githubAPI{hc: hc, base: ep.APIBase, token: tok}

	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	status, err := api.get(ctx, "/user", &u)
	switch {
	case err != nil:
		return Identity{}, err
	case status != 200:
		return Identity{}, &ProviderError{Op: "github user", Status: status}
	case u.ID <= 0 || u.Login == "" || len(u.Login) > 64:
		return Identity{}, &ProviderError{Op: "github user", Status: status, Desc: "no user id or login"}
	}
	id := Identity{Subject: strconv.FormatInt(u.ID, 10), Login: u.Login, Name: u.Name}

	verified, emailErr := api.verifiedEmails(ctx)
	if len(verified) > 0 {
		id.Email = verified[0]
	}
	for _, e := range verified {
		if listedAddress(r.Allow, e) {
			id.Email = e
			return id, nil
		}
	}

	restricted := false
	for _, org := range r.Orgs {
		var m struct {
			State        string `json:"state"`
			Organization struct {
				ID int64 `json:"id"`
			} `json:"organization"`
		}
		status, err := api.get(ctx, "/user/memberships/orgs/"+url.PathEscape(org), &m)
		switch {
		case err != nil:
			return id, err
		case status == 200 && m.State == "active" && r.pinned(org, m.Organization.ID):
			return id, nil
		case status == 200, status == 404:
		case status == 403:
			restricted = true
		default:
			return id, &ProviderError{Op: "github membership", Status: status}
		}
	}
	switch {
	// The addresses could not be read, so the list was never really
	// checked: that is GitHub failing, not the person being refused.
	case emailErr != nil && config.SignInListsAddress(r.Allow):
		return id, emailErr
	case restricted:
		return id, ErrOrgAccess
	}
	return id, fmt.Errorf("%w: no verified address listed exactly and no active membership of a listed organization", ErrNotAllowed)
}

// GitHubOrgID looks org up without a token, so the sign-in settings can
// pin it by id. ErrUnknownOrg when GitHub has no organization of that name.
func (c *Client) GitHubOrgID(ctx context.Context, ep Endpoints, org string) (int64, error) {
	hc, err := c.client("signin:github", ep)
	if err != nil {
		return 0, err
	}
	var o struct {
		ID int64 `json:"id"`
	}
	status, err := githubAPI{hc: hc, base: ep.APIBase}.get(ctx, "/orgs/"+url.PathEscape(org), &o)
	switch {
	case err != nil:
		return 0, err
	case status == http.StatusNotFound:
		return 0, ErrUnknownOrg
	case status != 200:
		return 0, &ProviderError{Op: "github org", Status: status}
	case o.ID <= 0:
		return 0, &ProviderError{Op: "github org", Status: status, Desc: "no organization id"}
	}
	return o.ID, nil
}

// verifiedEmails lists the account's verified addresses, the primary one
// first. Unverified addresses are dropped: anyone can add any address to a
// GitHub account.
func (g githubAPI) verifiedEmails(ctx context.Context) ([]string, error) {
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	status, err := g.get(ctx, "/user/emails?per_page=100", &emails)
	switch {
	case err != nil:
		return nil, err
	case status != 200:
		return nil, &ProviderError{Op: "github emails", Status: status}
	}
	var out []string
	for _, e := range emails {
		if !e.Verified || e.Email == "" || len(e.Email) > 320 {
			continue
		}
		if e.Primary {
			out = slices.Insert(out, 0, e.Email)
		} else {
			out = append(out, e.Email)
		}
	}
	return out, nil
}

// revoke deletes the person's token in the background: Bothy never keeps
// it, and the sign-in need not wait for GitHub to confirm.
func (c *Client) revoke(ctx context.Context, hc *http.Client, ep Endpoints, clientID string, secret core.Secret, tok string) {
	log := c.log()
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
		defer cancel()
		body, _ := json.Marshal(map[string]string{"access_token": tok})
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, ep.APIBase+"/applications/"+url.PathEscape(clientID)+"/token", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.SetBasicAuth(clientID, secret.Reveal())
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
		req.Header.Set("Content-Type", "application/json")
		status, _, err := do(ctx, hc, "github revoke", req, maxToken)
		if err == nil && status != http.StatusNoContent {
			err = fmt.Errorf("status %d", status)
		}
		if err != nil {
			log.Debug("sign-in: revoking the GitHub token failed", "err", err)
		}
	}()
}

// CheckGitHub posts a made-up code with the client's credentials: GitHub
// answers bad_verification_code only once it has accepted the client.
func (c *Client) CheckGitHub(ctx context.Context, ep Endpoints, clientID, redirect string, secret core.Secret) error {
	hc, err := c.client("signin:github", ep)
	if err != nil {
		return err
	}
	verifier, _ := NewPKCE()
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {secret.Reveal()},
		"code":          {"bothy-check"},
		"code_verifier": {verifier},
	}
	if redirect != "" {
		form.Set("redirect_uri", redirect)
	}
	_, err = githubToken(ctx, hc, ep, form)
	if errors.Is(err, ErrRejected) {
		return nil
	}
	return err
}
