package bedrock

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// contextRe adds Bedrock's own wording of a prompt over the model's window
// to the vendor-neutral patterns.
var contextRe = regexp.MustCompile(`(?i)input is too long|too many input tokens|exceeds? (the )?context|context limit`)

// classify reads AWS JSON errors: the code comes from X-Amzn-ErrorType
// ("ThrottlingException:http://internal.amazon.com/coral/…"), else from the
// body's __type (optionally namespaced "com.amazon…#Code") or code.
func classify(status int, h http.Header, body []byte) (core.ErrorKind, string) {
	var e struct {
		Type     string `json:"__type"`
		Code     string `json:"code"`
		Message  string `json:"message"`
		MessageU string `json:"Message"`
	}
	_ = json.Unmarshal(body, &e)
	code := errorCode(h.Get("X-Amzn-Errortype"))
	if code == "" {
		code = errorCode(e.Type)
	}
	if code == "" {
		code = errorCode(e.Code)
	}
	msg := e.Message
	if msg == "" {
		msg = e.MessageU
	}
	if msg == "" && code == "" {
		msg = strings.TrimSpace(string(body))
	}
	if code != "" && msg != "" {
		msg = code + ": " + msg
	} else if code != "" {
		msg = code
	}
	return codeKind(code, msg, status), msg
}

// errorCode strips the namespace and URI decorations AWS adds to a code.
func errorCode(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

// codeKind maps Bedrock Runtime, control-plane and AWS auth error codes
// (also the in-stream exception types, compared case-insensitively).
func codeKind(code, msg string, status int) core.ErrorKind {
	switch strings.ToLower(code) {
	case "throttlingexception", "servicequotaexceededexception", "toomanyrequestsexception", "throttling":
		return core.ErrKindRateLimit
	case "serviceunavailableexception", "modelnotreadyexception":
		return core.ErrKindOverloaded
	case "accessdeniedexception", "unrecognizedclientexception", "invalidsignatureexception",
		"incompletesignatureexception", "missingauthenticationtokenexception", "expiredtokenexception",
		"expiredtoken", "invalidclienttokenid", "signaturedoesnotmatch", "notauthorized":
		return core.ErrKindAuth
	case "validationexception":
		if contextRe.MatchString(msg) || wire.IsContextOverflow(msg) {
			return core.ErrKindContextLength
		}
		return core.ErrKindInvalidRequest
	case "resourcenotfoundexception", "conflictexception":
		return core.ErrKindInvalidRequest
	case "modeltimeoutexception", "modelerrorexception", "modelstreamerrorexception",
		"internalserverexception", "internalfailure":
		return core.ErrKindServer
	}
	if status == http.StatusOK {
		return core.ErrKindServer
	}
	return wire.StatusKind(status)
}

var (
	accountRe = regexp.MustCompile(`[0-9]{12,}`)
	// sessionRe finds the session of an assumed role and a federated user's
	// name: with SSO the session name is the person's sign-in, often an
	// email address.
	sessionRe = regexp.MustCompile(`(assumed-role/[\w+=,.@-]+/|federated-user/)[\w+=,.@-]+`)
)

// ScrubAccounts replaces 12-digit AWS account ids and role session names (in
// messages such as "User: arn:aws:sts::123456789012:assumed-role/Dev/jane@example.com
// is not authorized…") so provider errors, health state and logs, which
// members can see in a failed run, carry neither; the role name stays as
// the clue for fixing the policy.
func ScrubAccounts(s string) string {
	s = accountRe.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) == 12 {
			return "<account>"
		}
		return m
	})
	return sessionRe.ReplaceAllString(s, "${1}<session>")
}
