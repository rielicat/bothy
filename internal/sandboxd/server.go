package sandboxd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bothyhq/bothy/internal/sandboxd/engine"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

const (
	maxBody      = 8 << 20 // stdin (2 MiB, base64) plus argv and env, which JSON escaping can inflate 6x
	stdioUpgrade = "bothy-stdio/1"
)

func (d *daemon) routes() http.Handler {
	mux := http.NewServeMux()
	p := "/v" + proto.Version
	mux.HandleFunc("GET "+p+"/health", d.handleHealth)
	mux.HandleFunc("POST "+p+"/selftest", d.handleSelfTest)
	mux.HandleFunc("POST "+p+"/boxes", d.handleCreate)
	mux.HandleFunc("GET "+p+"/boxes", d.handleList)
	mux.HandleFunc("POST "+p+"/boxes/{key}/exec", d.handleExec)
	mux.HandleFunc("POST "+p+"/boxes/{key}/stdio", d.handleStdio)
	mux.HandleFunc("GET "+p+"/boxes/{key}/stderr", d.handleStderr)
	mux.HandleFunc("POST "+p+"/boxes/{key}/kill", d.handleKill)
	mux.HandleFunc("DELETE "+p+"/boxes/{key}", d.handleDelete)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, proto.ErrInvalid):
		ae = &apiError{http.StatusBadRequest, "bad_request", err.Error()}
	case errors.Is(err, engine.ErrNotFound):
		ae = &apiError{http.StatusNotFound, "not_found", err.Error()}
	default:
		ae = &apiError{http.StatusBadGateway, "engine", err.Error()}
	}
	writeJSON(w, ae.status, proto.Error{Code: ae.code, Message: ae.msg})
}

// decode reads exactly one JSON value, rejecting unknown fields and trailing
// data: the schema is fixed and anything else is a client bug or an attack.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", proto.ErrInvalid, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after the JSON body", proto.ErrInvalid)
	}
	return nil
}

func jsonLine(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func pathKey(r *http.Request) (string, error) {
	k := r.PathValue("key")
	if !proto.ValidKey(k) {
		return "", fmt.Errorf("%w: key must match ^box_[0-9a-z]{26}$", proto.ErrInvalid)
	}
	return k, nil
}

func (d *daemon) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, d.healthSnapshot())
}

func (d *daemon) handleSelfTest(w http.ResponseWriter, r *http.Request) {
	// A caller that hangs up must not leave a "context canceled" failure as
	// the published health; the test is bounded by its own timeout.
	d.selfTest(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusOK, d.healthSnapshot())
}

func (d *daemon) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req proto.CreateReq
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	resp, err := d.create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if resp.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, resp)
}

func (d *daemon) handleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, d.list())
}

func (d *daemon) handleExec(w http.ResponseWriter, r *http.Request) {
	key, err := pathKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req proto.ExecReq
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	p, err := d.planExec(&req)
	if err != nil {
		writeError(w, err)
		return
	}
	if ok, reason := d.healthy(); !ok {
		writeError(w, errUnavailable(reason))
		return
	}
	b, err := d.acquire(key, false)
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.release(b, false)
	run, err := d.beginExec(r.Context(), b, p)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	d.streamExec(run, b, p, &frameWriter{w: w, flush: rc.Flush})
}

func (d *daemon) handleStdio(w http.ResponseWriter, r *http.Request) {
	key, err := pathKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), stdioUpgrade) || !headerHasToken(r.Header, "Connection", "upgrade") {
		writeError(w, fmt.Errorf("%w: stdio needs Connection: Upgrade and Upgrade: %s", proto.ErrInvalid, stdioUpgrade))
		return
	}
	var req proto.StdioReq
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := validateStdio(&req); err != nil {
		writeError(w, err)
		return
	}
	if ok, reason := d.healthy(); !ok {
		writeError(w, errUnavailable(reason))
		return
	}
	b, err := d.acquire(key, true)
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.release(b, true)
	cwd, _ := proto.CleanCwd(req.Cwd)
	ctx := r.Context()
	execID, err := d.eng.ExecCreate(ctx, b.id, engine.ExecConfig{
		User: boxUser, WorkingDir: workDir(cwd), Env: envList(req.Env), Cmd: runCmd(req.Argv, d.cfg.MaxLifetime),
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		if errors.Is(err, engine.ErrNotFound) || errors.Is(err, engine.ErrConflict) {
			d.forget(b.key, b.id)
			writeError(w, errNotFound(key))
			return
		}
		writeError(w, errEngine(err))
		return
	}
	stream, err := d.eng.ExecStart(context.WithoutCancel(ctx), execID)
	if err != nil {
		writeError(w, errEngine(err))
		return
	}
	defer stream.Close()
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		writeError(w, err)
		return
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+stdioUpgrade+"\r\n\r\n"); err != nil {
		return
	}
	stdioPump(conn, brw.Reader, stream, b.stderr, d.stdioGrace)
}

func validateStdio(r *proto.StdioReq) error {
	if err := proto.ValidateArgv(r.Argv); err != nil {
		return err
	}
	if _, err := proto.CleanCwd(r.Cwd); err != nil {
		return err
	}
	return proto.ValidateEnv(r.Env)
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func (d *daemon) handleStderr(w http.ResponseWriter, r *http.Request) {
	key, err := pathKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := d.lookup(key)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b.stderr.Bytes())
}

func (d *daemon) handleKill(w http.ResponseWriter, r *http.Request) {
	key, err := pathKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	resp, err := d.kill(r.Context(), key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *daemon) handleDelete(w http.ResponseWriter, r *http.Request) {
	key, err := pathKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := d.destroy(r.Context(), key); err != nil {
		writeError(w, errEngine(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
