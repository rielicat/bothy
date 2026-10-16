package dto

import "github.com/rielicat/bothy/internal/core"

// ---- sign in with Google and GitHub (SPEC §15) ----

// SignInMethods is GET /api/auth/methods: what the sign-in page offers.
type SignInMethods struct {
	Providers []core.SignInOption `json:"providers"`
}

// SignInStartReq is POST /api/auth/oauth/{provider}/start.
type SignInStartReq struct {
	Next string `json:"next,omitempty"` // where to land after signing in; validated server-side
}

// SignInStartResp answers both starts (sign in and connect): the provider's
// authorize address, for window.location.assign.
type SignInStartResp struct {
	RedirectURL string `json:"redirect_url"`
}

// MySignIns is GET /api/me/sign-ins.
type MySignIns struct {
	Providers   []core.SignInOption `json:"providers"`    // turned on now; the person may connect these
	Linked      []*core.SignIn      `json:"linked"`       // their links, including providers since turned off
	HasPassword bool                `json:"has_password"` // false for accounts made by provider sign-in
}

// SignInAdmin is GET /api/sign-in and the answer to both PATCHes.
type SignInAdmin struct {
	PublicURL string                      `json:"public_url"`
	Address   string                      `json:"address"`   // ok | missing | not_https
	TeamMode  bool                        `json:"team_mode"` // allow_new takes effect only in team mode
	Allow     []string                    `json:"allow"`     // auth.sign_in.allow, shared by both providers
	AllowNew  bool                        `json:"allow_new"`
	Providers []core.SignInProviderStatus `json:"providers"`
}

// SignInListPatch is PATCH /api/sign-in: who may sign in. Nil fields stay.
// Allow entries are trimmed and lower-cased; empties and repeats dropped.
type SignInListPatch struct {
	Allow    *[]string `json:"allow,omitempty"`
	AllowNew *bool     `json:"allow_new,omitempty"`
}

// SignInPatch is PATCH /api/sign-in/{provider}; nil fields stay.
// ClientSecret is write-only and "" keeps the saved one. Orgs is GitHub
// only; the server pins each one by its GitHub id as it saves them.
type SignInPatch struct {
	Enabled      *bool     `json:"enabled,omitempty"`
	ClientID     *string   `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	Orgs         *[]string `json:"orgs,omitempty"`
}

// SignInCheckReq is POST /api/sign-in/{provider}/check; "" means the saved
// value.
type SignInCheckReq struct {
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// SignInCheck says whether the provider accepted the client id and secret.
type SignInCheck struct {
	OK    bool             `json:"ok"`
	Code  core.FailureCode `json:"code,omitempty"`  // key_rejected | provider_error | timeout | unreachable | tls | private_address | unknown
	Error string           `json:"error,omitempty"` // raw message, for Details
}
