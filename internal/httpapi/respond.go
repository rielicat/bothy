package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// apiError is an error with a fixed HTTP status and envelope code.
type apiError struct {
	status int
	code   string
	msg    string
	fix    string
	retry  time.Duration
}

func (e *apiError) Error() string { return e.msg }

func newError(status int, code, msg string) *apiError {
	return &apiError{status: status, code: code, msg: msg}
}

func (e *apiError) withFix(fix string) *apiError { e.fix = fix; return e }

func badRequest(format string, args ...any) error {
	return newError(http.StatusBadRequest, "bad_request", fmt.Sprintf(format, args...))
}

var (
	errNotFound     = newError(http.StatusNotFound, "not_found", "not found")
	errUnauthorized = newError(http.StatusUnauthorized, "unauthorized", "sign in required")
	errCookieOnly   = newError(http.StatusForbidden, "forbidden", "this endpoint needs a signed-in browser session, not an API token")
	errSafeMode     = newError(http.StatusServiceUnavailable, "unavailable", "safe mode: fix bothy.yaml").withFix("bothy config check (Docker: docker compose exec bothy /bothy config check), then restart")
)

// errorEnvelope is dto.Error plus the field errors of a rejected config
// change, which dto.ErrorBody has no room for.
type errorEnvelope struct {
	dto.Error
	Fields []config.FieldError `json:"fields,omitempty"`
}

// mapError turns any error into status, envelope and Retry-After. Messages
// of 404 and 500 are generic so they reveal neither existence nor internals.
func (s *Server) mapError(err error) (int, errorEnvelope, time.Duration) {
	body := func(code, msg, fix string) errorEnvelope {
		return errorEnvelope{Error: dto.Error{Error: dto.ErrorBody{Code: code, Message: s.redact(msg), Fix: fix}}}
	}
	var ae *apiError
	var fe config.FieldErrors
	var be *core.BudgetError
	var mbe *http.MaxBytesError
	var limitErr interface{ RetryAfter() time.Duration }
	var authErr interface{ Unauthenticated() bool }
	switch {
	case errors.As(err, &ae):
		return ae.status, body(ae.code, ae.msg, ae.fix), ae.retry
	case errors.As(err, &fe):
		env := body("bad_request", fe.Error(), "")
		env.Fields = fe
		return http.StatusBadRequest, env, 0
	case errors.As(err, &mbe):
		return http.StatusRequestEntityTooLarge, body("bad_request", fmt.Sprintf("request body larger than %d bytes", mbe.Limit), ""), 0
	case errors.As(err, &authErr) && authErr.Unauthenticated():
		return http.StatusUnauthorized, body("unauthorized", err.Error(), ""), 0
	case errors.As(err, &limitErr):
		return http.StatusTooManyRequests, body("rate_limited", err.Error(), ""), limitErr.RetryAfter()
	case errors.As(err, &be) && be.Kind == core.BudgetDailyCost:
		return http.StatusTooManyRequests, body("budget_exceeded", "Bothy has reached today's spending limit. It can start again at midnight.", "/settings/general"), 0
	case errors.As(err, &be):
		return http.StatusTooManyRequests, body("budget_exceeded", "Bothy stopped because this task reached its limit.", "/settings/advanced"), 0
	case errors.Is(err, core.ErrNotFound):
		return http.StatusNotFound, body("not_found", "not found", ""), 0
	case errors.Is(err, core.ErrForbidden):
		return http.StatusForbidden, body("forbidden", bare(err, core.ErrForbidden), ""), 0
	case errors.Is(err, core.ErrConflict):
		return http.StatusConflict, body("conflict", bare(err, core.ErrConflict), ""), 0
	case errors.Is(err, core.ErrInvalid):
		return http.StatusBadRequest, body("bad_request", bare(err, core.ErrInvalid), ""), 0
	case errors.Is(err, core.ErrBusy):
		return http.StatusTooManyRequests, body("busy", "busy: send stop to interrupt the current run", ""), time.Second
	case errors.Is(err, core.ErrPaused):
		return http.StatusServiceUnavailable, body("paused", "Bothy is paused by an admin", ""), 0
	case errors.Is(err, core.ErrSealed):
		return http.StatusServiceUnavailable, body("sealed", "Bothy's saved keys are locked.", "/settings/security/keys"), 0
	case errors.Is(err, core.ErrNoSandbox):
		return http.StatusServiceUnavailable, body("no_sandbox", "Bothy can't run code on this server.", "/settings/security"), 0
	case errors.Is(err, core.ErrNotImplemented):
		return http.StatusServiceUnavailable, body("unavailable", "not available in this build", ""), 0
	}
	return http.StatusInternalServerError, body("internal", "internal error", ""), 0
}

// bare is err's message without the sentinel's own word ("forbidden: admin
// only" → "admin only"), which the envelope code already says.
func bare(err, sentinel error) string {
	msg := strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
	return strings.TrimSuffix(msg, ": "+sentinel.Error())
}

func (s *Server) redact(msg string) string {
	if s.d.Vault == nil {
		return msg
	}
	return s.d.Vault.Redactor().Redact(msg)
}

// errAborted marks a failure after the response started: nothing more can
// be sent, so writeError only logs it.
var errAborted = errors.New("response aborted")

func aborted(err error) error { return fmt.Errorf("%w: %w", errAborted, err) }

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return
	}
	if errors.Is(err, errAborted) {
		s.log.Debug("response aborted", "path", r.URL.Path, "request_id", infoOf(r).id, "err", err)
		return
	}
	status, env, retry := s.mapError(err)
	if status == http.StatusInternalServerError {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "request_id", infoOf(r).id, "err", err)
	}
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(max(retry.Round(time.Second), time.Second).Seconds())))
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		writeJSON(w, status, openAIError(status, env.Error.Error.Code, env.Error.Error.Message, ""))
		return
	}
	writeJSON(w, status, env)
}

// writeJSON encodes before writing so a value that cannot be encoded becomes
// a 500 instead of a 200 with an empty body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		buf.Reset()
		status = http.StatusInternalServerError
		buf.WriteString(`{"error":{"code":"internal","message":"internal error: response encoding failed"}}` + "\n")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func ok(w http.ResponseWriter, v any) error {
	writeJSON(w, http.StatusOK, v)
	return nil
}

func noContent(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func accepted(w http.ResponseWriter, v any) error {
	if v == nil {
		w.WriteHeader(http.StatusAccepted)
		return nil
	}
	writeJSON(w, http.StatusAccepted, v)
	return nil
}

// decode reads one JSON value; the body is already capped by the route.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		if errors.Is(err, io.EOF) {
			return badRequest("request body is empty")
		}
		return badRequest("invalid JSON: %v", err)
	}
	if dec.More() {
		return badRequest("invalid JSON: trailing data")
	}
	return nil
}

// nonNil makes JSON lists render as [] rather than null.
func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// page reads ?before=&limit=.
func page(r *http.Request) (core.Page, error) {
	var p core.Page
	q := r.URL.Query()
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return p, badRequest("before must be a non-negative integer")
		}
		p.Before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return p, badRequest("limit must be between 1 and 500")
		}
		p.Limit = n
	}
	return p, nil
}

func pageLimit(p core.Page) int {
	if p.Limit <= 0 {
		return 50
	}
	return p.Limit
}

// listOf builds dto.List with Next set to the key of the last item when the
// page is full.
func listOf[T any](items []T, p core.Page, key func(T) int64) dto.List[T] {
	l := dto.List[T]{Items: nonNil(items)}
	if len(items) > 0 && len(items) >= pageLimit(p) {
		l.Next = key(items[len(items)-1])
	}
	return l
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func boolParam(r *http.Request, name string) (bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, badRequest("%s must be true or false", name)
	}
	return b, nil
}
