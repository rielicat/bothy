package config

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	signInDomainRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	signInLocalRe  = regexp.MustCompile(`^[\x21-\x3f\x41-\x7e]{1,64}$`) // printable ASCII without spaces or '@'
	githubOrgRe    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)
	clientIDRe     = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
)

const (
	maxSignInAllow = 200
	MaxSignInOrgs  = 20
)

// publicMailDomains hand out addresses to anyone who asks (relays and
// GitHub's noreply addresses included), so listing one as a domain would
// let strangers in. An exact address there is fine.
var publicMailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true, "msn.com": true,
	"yahoo.com": true, "icloud.com": true, "me.com": true, "mac.com": true, "aol.com": true, "proton.me": true,
	"protonmail.com": true, "gmx.com": true, "mail.com": true, "yandex.com": true, "qq.com": true,
	"users.noreply.github.com": true, "duck.com": true, "privaterelay.appleid.com": true, "pm.me": true, "fastmail.com": true,
	"hey.com": true, "yahoo.co.uk": true, "yahoo.co.jp": true, "hotmail.co.uk": true, "hotmail.fr": true, "outlook.fr": true,
	"live.co.uk": true, "gmx.de": true, "gmx.net": true, "web.de": true, "mail.ru": true, "163.com": true, "126.com": true,
	"naver.com": true, "zoho.com": true, "tutanota.com": true, "yandex.ru": true, "rambler.ru": true, "libero.it": true,
	"orange.fr": true, "free.fr": true, "t-online.de": true, "btinternet.com": true, "comcast.net": true, "verizon.net": true,
	"att.net": true,
}

// SignInEntryProblem says why e cannot be listed in auth.sign_in.allow, or
// returns "". An entry is a domain ("acme.com") or an exact address
// ("pat@gmail.com"), in lower case.
func SignInEntryProblem(e string) string {
	if e != strings.ToLower(e) {
		return fmt.Sprintf("write %q in lower case", e)
	}
	local, domain, address := strings.Cut(e, "@")
	if !address {
		switch {
		case !signInDomainRe.MatchString(e):
			return fmt.Sprintf("%q is neither an email address nor a domain like acme.com", e)
		case publicMailDomains[e]:
			return fmt.Sprintf("anyone can get an address at %s; list each person's address instead", e)
		}
		return ""
	}
	if !signInLocalRe.MatchString(local) || !signInDomainRe.MatchString(domain) {
		return fmt.Sprintf("%q is not an email address like pat@acme.com", e)
	}
	return ""
}

// SignInListsAddress reports whether allow has an exact address, the only
// kind of entry GitHub sign-in matches.
func SignInListsAddress(allow []string) bool {
	return slices.ContainsFunc(allow, func(e string) bool { return strings.Contains(e, "@") })
}

// ValidGitHubOrg reports whether o has the shape of a GitHub organization.
func ValidGitHubOrg(o string) bool { return githubOrgRe.MatchString(o) }

// SignInAddress reports whether public_url can carry provider sign-in
// redirects: https, or http to a loopback host (development and tests).
// problem is "" | "missing" | "not_https"; origin is scheme://host[:port].
func SignInAddress(publicURL string) (origin, problem string) {
	if strings.TrimSpace(publicURL) == "" {
		return "", "missing"
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return "", "missing"
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && PlainHTTPAllowed(u.Hostname()):
	default:
		return "", "not_https"
	}
	return u.Scheme + "://" + u.Host, ""
}

// SignInRedirect is <origin>/api/auth/oauth/<provider>/callback, or "" when
// SignInAddress has a problem. Only the scheme and host of public_url count:
// Bothy is never served under a path prefix.
func SignInRedirect(publicURL, provider string) string {
	origin, problem := SignInAddress(publicURL)
	if problem != "" {
		return ""
	}
	return origin + "/api/auth/oauth/" + provider + "/callback"
}

func validateSignIn(c *Config, v *vctx) {
	si := c.Auth.SignIn
	g, h := si.Google, si.GitHub
	gp, hp := "auth.sign_in.google", "auth.sign_in.github"

	signInClient(v, gp, g.ClientID, g.APIBase)
	signInClient(v, hp, h.ClientID, h.APIBase)

	seen := map[string]bool{}
	for i, e := range si.Allow {
		switch msg := SignInEntryProblem(e); {
		case msg != "":
			v.addAt("auth.sign_in.allow", i, "%s", msg)
		case seen[e]:
			v.addAt("auth.sign_in.allow", i, "%q is listed twice", e)
		}
		seen[e] = true
	}
	if len(si.Allow) > maxSignInAllow {
		v.add("auth.sign_in.allow", "at most %d entries, got %d", maxSignInAllow, len(si.Allow))
	}
	seen = map[string]bool{}
	for i, o := range h.Orgs {
		k := strings.ToLower(o)
		switch {
		case !githubOrgRe.MatchString(o):
			v.addAt(hp+".orgs", i, "%q is not a GitHub organization name", o)
		case seen[k]:
			v.addAt(hp+".orgs", i, "duplicate organization %q", o)
		}
		seen[k] = true
	}
	if len(h.Orgs) > MaxSignInOrgs {
		v.add(hp+".orgs", "at most %d organizations, got %d", MaxSignInOrgs, len(h.Orgs))
	}
	if len(h.OrgIDs) > 0 && len(h.OrgIDs) != len(h.Orgs) {
		v.add(hp+".org_ids", "must hold one id per organization in %s.orgs (got %d for %d); save the organizations in Settings → Security to pin them again",
			hp, len(h.OrgIDs), len(h.Orgs))
	}
	for i, id := range h.OrgIDs {
		if id <= 0 {
			v.addAt(hp+".org_ids", i, "must be a GitHub organization id, a positive number")
		}
	}

	// Every problem of an enabled provider is reported on its enabled key, so
	// a broken combination falls back to "off" instead of half-working.
	_, problem := SignInAddress(c.Server.PublicURL)
	signInEnabled(v, gp, "Google", "google", g.Enabled, g.ClientID, g.ClientSecret, len(si.Allow) > 0,
		"at least one address or domain in auth.sign_in.allow", problem)
	// GitHub ignores domain entries (see oauth.Rules), so a list of domains
	// alone lets nobody in with it.
	signInEnabled(v, hp, "GitHub", "github", h.Enabled, h.ClientID, h.ClientSecret, SignInListsAddress(si.Allow) || len(h.Orgs) > 0,
		"at least one email address in auth.sign_in.allow (domains apply to Google only), or an organization in "+hp+".orgs", problem)
}

func signInClient(v *vctx, p, clientID, apiBase string) {
	if clientID != "" && !clientIDRe.MatchString(clientID) {
		v.add(p+".client_id", "must be printable characters without spaces, at most 256")
	}
	if apiBase == "" {
		return
	}
	if msg := urlProblem(apiBase, "http", "https"); msg != "" {
		v.add(p+".api_base", "%s", msg)
		return
	}
	u, _ := url.Parse(apiBase)
	switch {
	case u.RawQuery != "" || u.Fragment != "":
		v.add(p+".api_base", "must not have a query or fragment")
	case !PlainHTTPAllowed(u.Hostname()):
		v.add(p+".api_base", "only a loopback address is accepted; this exists for tests")
	}
}

func signInEnabled(v *vctx, p, name, id string, enabled bool, clientID, secret string, rules bool, rulesMsg, address string) {
	if !enabled {
		return
	}
	at := p + ".enabled"
	if clientID == "" {
		v.add(at, "%s sign-in needs %s.client_id", name, p)
	}
	if secret == "" {
		v.add(at, "%s sign-in needs %s.client_secret (a secret:<name> reference)", name, p)
	}
	if !rules {
		v.add(at, "%s sign-in needs %s", name, rulesMsg)
	}
	switch address {
	case "missing":
		v.add(at, "%s sign-in needs server.public_url: %s sends people back to <public_url>/api/auth/oauth/%s/callback", name, name, id)
	case "not_https":
		v.add(at, "%s sign-in needs server.public_url to start with https:// (http only for a loopback address)", name)
	}
}
