// Package actionstest fakes the GitHub Actions REST endpoints release_deploy
// uses: workflow_dispatch, a workflow's runs, a run's status and its jobs.
// It checks the bearer token and API version header like GitHub does and
// lets tests move runs through their states.
package actionstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Dispatch struct {
	Repo, Workflow, Ref string
	Inputs              map[string]string
	ReturnRunDetails    bool
}

type Run struct {
	ID                  int64
	Number              int
	Repo, Workflow, Ref string
	Status, Conclusion  string // conclusion "" while the run is not completed
	HeadSHA             string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	FailedStep          string // the step a failed run's job reports as failed
}

type Server struct {
	srv   *httptest.Server
	token string

	mu         sync.Mutex
	legacy     bool
	strict     bool
	rejectRefs []string
	dispatches []Dispatch
	runs       []*Run
	requests   int
	messages   map[string]string // commit sha -> message
}

// New starts a fake that accepts only "Bearer <token>".
func New(token string) *Server {
	s := &Server{token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{owner}/{repo}/actions/workflows/{wf}/dispatches", s.dispatch)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/workflows/{wf}/runs", s.list)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs/{id}", s.get)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs/{id}/jobs", s.jobs)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}", s.commit)
	s.srv = httptest.NewServer(s.auth(mux))
	return s
}

func (s *Server) URL() string { return s.srv.URL }

// SetCommitMessage gives a commit the message GET commits/{sha} returns.
func (s *Server) SetCommitMessage(sha, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages == nil {
		s.messages = map[string]string{}
	}
	s.messages[sha] = msg
}

func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	msg, ok := s.messages[r.PathValue("sha")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": r.PathValue("sha"), "commit": map[string]any{"message": msg}})
}

func (s *Server) Close() { s.srv.Close() }

// SetLegacy makes dispatches answer 204 without a run id.
func (s *Server) SetLegacy(on bool) {
	s.mu.Lock()
	s.legacy = on
	s.mu.Unlock()
}

// SetStrict makes dispatches reject body keys the server does not know with
// 422, as a GitHub Enterprise Server release without return_run_details
// does, and answer 204 otherwise.
func (s *Server) SetStrict(on bool) {
	s.mu.Lock()
	s.strict = on
	s.mu.Unlock()
}

// RejectRef makes dispatches of ref fail with 422, as for a missing branch.
func (s *Server) RejectRef(ref string) {
	s.mu.Lock()
	s.rejectRefs = append(s.rejectRefs, ref)
	s.mu.Unlock()
}

func (s *Server) Dispatches() []Dispatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dispatches)
}

// Requests counts every authenticated request.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *Server) Runs() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Run, len(s.runs))
	for i, r := range s.runs {
		out[i] = *r
	}
	return out
}

// SetRun moves a run to status (and conclusion once completed).
func (s *Server) SetRun(id int64, status, conclusion string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.ID == id {
			r.Status, r.Conclusion, r.UpdatedAt = status, conclusion, time.Now().UTC()
		}
	}
}

// FinishRun completes a run that took the given time, failing at step
// when step is not empty.
func (s *Server) FinishRun(id int64, conclusion string, took time.Duration, step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.ID == id {
			r.Status, r.Conclusion, r.UpdatedAt, r.FailedStep = "completed", conclusion, r.CreatedAt.Add(took), step
		}
	}
}

// DeleteRun makes a run unknown (404), as after the run was deleted.
func (s *Server) DeleteRun(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = slices.DeleteFunc(s.runs, func(r *Run) bool { return r.ID == id })
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
			return
		}
		if r.Header.Get("X-GitHub-Api-Version") == "" || !strings.Contains(r.Header.Get("Accept"), "application/vnd.github+json") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "missing API version or media type"})
			return
		}
		s.mu.Lock()
		s.requests++
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Problems parsing JSON"})
		return
	}
	var body struct {
		Ref              string            `json:"ref"`
		Inputs           map[string]string `json:"inputs"`
		ReturnRunDetails bool              `json:"return_run_details"`
	}
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &body); err != nil || body.Ref == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request: ref is required"})
		return
	}
	s.mu.Lock()
	if _, ok := raw["return_run_details"]; ok && s.strict {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request.\n\n\"return_run_details\" is not a permitted key."})
		return
	}
	if slices.Contains(s.rejectRefs, body.Ref) {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "No ref found for: " + body.Ref})
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	s.dispatches = append(s.dispatches, Dispatch{Repo: repo, Workflow: r.PathValue("wf"), Ref: body.Ref, Inputs: body.Inputs,
		ReturnRunDetails: body.ReturnRunDetails})
	now := time.Now().UTC()
	run := &Run{ID: int64(9000 + len(s.runs) + 1), Number: len(s.runs) + 1, Repo: repo, Workflow: r.PathValue("wf"), Ref: body.Ref,
		Status: "queued", HeadSHA: strings.Repeat("ab", 20), CreatedAt: now, UpdatedAt: now}
	s.runs = append(s.runs, run)
	legacy := s.legacy || s.strict
	s.mu.Unlock()
	if legacy || !body.ReturnRunDetails {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workflow_run_id": run.ID,
		"run_url":         fmt.Sprintf("%s/repos/%s/actions/runs/%d", s.srv.URL, repo, run.ID),
		"html_url":        fmt.Sprintf("%s/%s/actions/runs/%d", s.srv.URL, repo, run.ID),
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	repo, wf := r.PathValue("owner")+"/"+r.PathValue("repo"), r.PathValue("wf")
	q := r.URL.Query()
	var since time.Time
	if c, ok := strings.CutPrefix(q.Get("created"), ">="); ok {
		since, _ = time.Parse(time.RFC3339, c)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for i := len(s.runs) - 1; i >= 0; i-- {
		run := s.runs[i]
		if run.Repo != repo || run.Workflow != wf || (q.Get("branch") != "" && run.Ref != q.Get("branch")) || run.CreatedAt.Before(since) {
			continue
		}
		out = append(out, s.runJSON(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(out), "workflow_runs": out})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		if run.ID == id && run.Repo == repo {
			writeJSON(w, http.StatusOK, s.runJSON(run))
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

// jobs lists one job whose steps end at the run's failed step, if any.
func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		if run.ID != id || run.Repo != repo {
			continue
		}
		steps := []map[string]any{{"number": 1, "name": "Set up job", "conclusion": "success"}}
		if run.FailedStep != "" {
			steps = append(steps, map[string]any{"number": 2, "name": run.FailedStep, "conclusion": "failure"},
				map[string]any{"number": 3, "name": "Complete job", "conclusion": "skipped"})
		}
		var conclusion any
		if run.Conclusion != "" {
			conclusion = run.Conclusion
		}
		writeJSON(w, http.StatusOK, map[string]any{"total_count": 1, "jobs": []map[string]any{
			{"id": run.ID*10 + 1, "name": "deploy", "status": run.Status, "conclusion": conclusion, "steps": steps}}})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func (s *Server) runJSON(r *Run) map[string]any {
	var conclusion any
	if r.Conclusion != "" {
		conclusion = r.Conclusion
	}
	return map[string]any{
		"id": r.ID, "run_number": r.Number, "name": "Deploy (untrusted name)", "display_title": "commit message",
		"event": "workflow_dispatch", "head_branch": r.Ref, "head_sha": r.HeadSHA, "status": r.Status, "conclusion": conclusion,
		"html_url":   fmt.Sprintf("https://evil.example/%s/actions/runs/%d", r.Repo, r.ID),
		"created_at": r.CreatedAt.Format(time.RFC3339), "updated_at": r.UpdatedAt.Format(time.RFC3339),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
