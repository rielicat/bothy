package channel

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	placeholderText = "Working…"
	defaultOpGap    = time.Second
)

// StreamTo returns a sink that delivers one run to chat. Mode "stream" shows
// a live message edited no faster than the channel allows; "final" (schedule
// delivery) sends the reply once the run ends.
func (h *Hub) StreamTo(chat core.ChatRef, mode string) core.RunSink {
	if mode != "final" {
		mode = "stream"
	}
	return &streamSink{h: h, chat: chat, mode: mode, wake: make(chan struct{}, 1)}
}

// streamSink buffers events without blocking the runner; a hub goroutine,
// started on the first event, turns them into platform sends and edits.
type streamSink struct {
	h    *Hub
	chat core.ChatRef
	mode string
	wake chan struct{}

	mu      sync.Mutex
	events  []core.Event
	started bool
	closed  bool
}

func (s *streamSink) OnEvent(e core.Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.events = append(s.events, e)
	start := !s.started
	s.started = true
	s.mu.Unlock()
	if start && !s.h.goAsync(s.run) {
		s.close()
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *streamSink) take() []core.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := s.events
	s.events = nil
	return ev
}

// close detaches the sink: later events (for example after a stop that was
// not honoured in time) are ignored.
func (s *streamSink) close() {
	s.mu.Lock()
	s.closed, s.events = true, nil
	s.mu.Unlock()
}

func (s *streamSink) run(ctx context.Context) {
	defer s.close()
	a := s.h.live(s.chat.Channel)
	if a == nil {
		return
	}
	st := &streamState{s: s, ch: a.ch, caps: a.ch.Caps(), started: time.Now(), final: s.mode == "final"}
	if !st.caps.Edit {
		st.final = true
	}
	st.gap = st.caps.EditMinInterval
	if st.gap <= 0 {
		st.gap = defaultOpGap
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	idle := time.Now().Add(s.h.sinkIdle)
	for {
		if evs := s.take(); len(evs) > 0 {
			for _, e := range evs {
				st.apply(ctx, e)
			}
			idle = time.Now().Add(s.h.sinkIdle)
		}
		if st.done {
			st.finish(ctx)
			return
		}
		st.step(ctx)
		timer.Reset(max(time.Until(earliest(st.nextWake(), idle)), 0))
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
			if !time.Now().Before(idle) {
				st.finish(ctx)
				return
			}
		}
	}
}

type streamState struct {
	s     *streamSink
	ch    core.Channel
	caps  core.ChannelCaps
	final bool
	gap   time.Duration

	run     core.RunID
	session core.SessionID
	exposed bool

	raw       strings.Builder // the run's reply so far, redacted
	tail      string          // newest model text, held back until streamCut releases it into raw
	commit    int             // bytes of raw already shown in finished messages
	carry     string          // fence line the next message must reopen
	stepBreak bool            // a tool call ended the model's last text; the next text starts a paragraph
	lastStep  int             // offset in raw of the latest step's text

	msgs       []core.MsgRef // messages sent for this run; the last one is live
	shown      string        // text the live message shows
	needLive   bool          // the live message was finalized; the next op sends a new one
	status     string
	toolStatus string // status line of the latest tool call, restored after an approval wait
	statusKind string
	gotDelta   bool

	started    time.Time
	lastOp     time.Time
	nextTyping time.Time

	done     bool
	parked   bool
	finished *core.RunFinishedData
	notices  []string
	defanged bool // the reply had links neutralized
}

func (st *streamState) apply(ctx context.Context, e core.Event) {
	if st.run == "" && e.Run != "" {
		st.run = e.Run
	}
	if st.session == "" && e.Session != "" {
		st.session = e.Session
		st.refreshTaint(ctx)
	}
	switch d := e.Data.(type) {
	case core.RunStartedData:
		st.statusKind = "thinking"
	case core.RunStatusData:
		st.statusKind = d.Status
		switch d.Status {
		case "waiting_approval":
			st.status = "_Waiting for approval…_"
		case "compacting":
			st.status = "_Summarising older messages…_"
		case "tool":
			st.status = st.toolStatus
		case "thinking":
			st.status = ""
			if d.Detail == core.RefineStatusDetail {
				st.status = "_" + core.RefineStatusDetail + "…_"
			}
		}
	case core.RunQueuedData:
		st.status = fmt.Sprintf("_Queued (%d ahead)…_", d.Position)
	case core.DeltaData:
		if e.Type == core.EvRunDelta && d.Text != "" {
			// A refined answer arrives only once refining is done.
			if st.statusKind == "thinking" {
				st.status = ""
			}
			st.appendText(d.Text)
		}
	case core.ToolCallData:
		st.releaseTail()
		st.toolStatus = "_" + toolDoing(d, st.s.h.redact) + "…_"
		st.status = st.toolStatus
		st.stepBreak = true
	case core.ToolResultData:
		st.status = ""
		// A tool result is where taint changes; the session may have just
		// become Exposed, and live edits must start neutralizing links now.
		st.refreshTaint(ctx)
	case core.SessionData:
		st.exposed = st.exposed || d.Taint.Exposed()
	case core.MessageData:
		if d.Message != nil && d.Message.Kind == core.KindNotice {
			if t := strings.TrimSpace(d.Message.PlainText()); t != "" {
				st.notices = append(st.notices, t)
			}
		}
	case core.RunParkedData:
		st.parked, st.done = true, true
	case core.RunFinishedData:
		st.finished, st.done = &d, true
	}
}

// appendText adds model text to the reply. Each step's text is its own
// assistant message, so text after a tool call starts a new paragraph rather
// than running into the previous step's last sentence. Text reaches raw only
// redacted and once no later delta can complete a secret in it, so neither a
// live edit nor a split into several messages can show part of one.
func (st *streamState) appendText(text string) {
	if st.stepBreak {
		st.stepBreak = false
		st.releaseTail()
		raw := st.raw.String()
		switch {
		case raw == "", strings.HasSuffix(raw, "\n\n"):
		case strings.HasSuffix(raw, "\n"):
			st.raw.WriteByte('\n')
		default:
			st.raw.WriteString("\n\n")
		}
		st.lastStep = st.raw.Len()
	}
	st.tail += text
	cut := st.s.h.streamCut(st.tail)
	st.raw.WriteString(st.s.h.redact(st.tail[:cut]))
	st.tail = st.tail[cut:]
	st.gotDelta = true
}

// releaseTail moves the held-back text into raw once nothing can follow it:
// the step or the run ended.
func (st *streamState) releaseTail() {
	if st.tail != "" {
		st.raw.WriteString(st.s.h.redact(st.tail))
		st.tail = ""
	}
}

// partialer is implemented by redactors that know the registered secrets
// (vault.Redactor): PartialAt finds a tail of s that could still grow into
// one.
type partialer interface{ PartialAt(s string) int }

// streamCut returns how much of the model's newest text can be redacted and
// kept: up to whitespace, so a token-shaped secret still arriving is never
// split, and before any tail a registered secret (which may contain spaces)
// could still be completing.
func (h *Hub) streamCut(s string) int {
	cut := afterLastSpace(s)
	p, ok := h.d.Redactor.(partialer)
	for ok && cut > 0 {
		at := p.PartialAt(s[:cut])
		if at >= cut {
			break
		}
		cut = afterLastSpace(s[:at])
	}
	return cut
}

func afterLastSpace(s string) int {
	i := strings.LastIndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return 0
	}
	_, n := utf8.DecodeRuneInString(s[i:])
	return i + n
}

func (st *streamState) refreshTaint(ctx context.Context) {
	if st.session == "" {
		return
	}
	if sess, err := st.s.h.d.Store.Sessions().Get(ctx, core.SystemViewer(), st.session); err == nil {
		st.exposed = st.exposed || sess.Taint.Exposed()
	}
}

// toolDoing is a tool call's status line in the words of the web's activity
// lines (docs/design/ui.md §7.4): "Running `pip install pandas`", "Deleting
// `notes/a.md`", "Searching the web for “pandas”"; a tool without such a
// phrase shows its title. Paths, hosts and repositories go in code spans, so
// none of them is a tappable link. Values are redacted before they are
// shortened, since a cut secret no longer matches the redactor.
func toolDoing(d core.ToolCallData, redact func(string) string) string {
	a := d.Action
	if a == nil {
		a = &core.Action{}
	}
	var args map[string]any
	_ = json.Unmarshal(a.Payload, &args)
	arg := func(k string) string { s, _ := args[k].(string); return strings.TrimSpace(s) }
	code := func(s string) string {
		s, _, _ = strings.Cut(strings.TrimSpace(redact(s)), "\n")
		return codeSpan(truncateRunes(s, 60))
	}
	quote := func(s string) string { return "“" + escapeMD(truncateRunes(oneLine(redact(s)), 60)) + "”" }
	path := ""
	if len(a.Paths) > 0 {
		path = a.Paths[0]
	}
	host := ""
	if u, err := url.Parse(a.URL); err == nil {
		host = u.Hostname()
	}
	switch {
	case d.Tool == "python":
		return "Running a Python script"
	case a.Kind == core.ActExec && len(a.Argv) > 0:
		cmd := strings.Join(a.Argv, " ")
		if len(a.Argv) >= 3 && (a.Argv[1] == "-lc" || a.Argv[1] == "-c") {
			cmd = a.Argv[2]
		}
		return "Running " + code(cmd)
	case path != "" && (d.Tool == "fs_read" || d.Tool == "fs_list" || d.Tool == "fs_write" || d.Tool == "fs_delete"):
		verb := map[string]string{"fs_read": "Reading ", "fs_list": "Looking in ", "fs_write": "Saving ", "fs_delete": "Deleting "}[d.Tool]
		if d.Tool == "fs_list" && path == "." {
			return "Looking in Bothy's files"
		}
		return verb + code(path)
	case arg("query") != "" && (d.Tool == "web_search" || d.Tool == "session_search" || d.Tool == "knowledge_search"):
		where := map[string]string{"web_search": "the web", "session_search": "past chats", "knowledge_search": "project knowledge"}[d.Tool]
		return "Searching " + where + " for " + quote(arg("query"))
	case d.Tool == "web_fetch" && host != "":
		if a.Method != "" && !strings.EqualFold(a.Method, "GET") {
			return "Sending data to " + code(host)
		}
		return "Opening " + code(host)
	case d.Tool == "message":
		return "Sending a message"
	case d.Tool == "repo_checkout" && arg("repo") != "":
		if ref := arg("ref"); ref != "" {
			return "Getting a copy of " + code(arg("repo")) + " (" + code(ref) + ")"
		}
		return "Getting a copy of " + code(arg("repo"))
	case d.Tool == "repo_open_pr" && arg("repo") != "":
		return "Opening a pull request in " + code(arg("repo"))
	case d.Tool == "release_deploy" && arg("repo") != "" && arg("environment") != "":
		return "Deploying " + code(arg("repo")) + " to " + code(arg("environment"))
	}
	title := cmp.Or(d.Title, d.Tool)
	if a.Kind == core.ActMCP {
		title = strings.TrimPrefix(title, "MCP "+a.MCPServer+": ")
	}
	return escapeMD(truncateRunes(oneLine(redact(title)), 60))
}

// render applies the channel output rules: redaction always, link
// neutralization when the session is Exposed.
func (st *streamState) render(md string) string {
	out := st.s.h.redact(md)
	if st.exposed {
		out = core.NeutralizeMarkdown(out)
	}
	return out
}

// statusLine is the rendered status line: a search query in it is prose,
// so its links are neutralized like the reply's.
func (st *streamState) statusLine() string {
	if st.status == "" {
		return ""
	}
	return st.render(st.status)
}

func (st *streamState) buttons() [][]core.Button {
	if !st.caps.Buttons || st.run == "" {
		return nil
	}
	return [][]core.Button{{{Label: "Stop", Data: "s:" + string(st.run)}}}
}

// pending is the not-yet-finalized part of the reply. While the model is
// still writing, the trailing partial word is held back too, so a link still
// arriving is neutralized whole. Once a tool call has ended the step, the
// runner has flushed all of that step's text and releaseTail has moved it
// into raw, so nothing is held back while the tool runs or awaits approval.
func (st *streamState) pending(live bool) string {
	seg := st.raw.String()[st.commit:]
	if live && !st.stepBreak {
		if i := strings.LastIndexFunc(seg, unicode.IsSpace); i >= 0 {
			seg = seg[:i]
		} else {
			seg = ""
		}
	}
	return seg
}

// firstPiece splits off the first message-sized piece of seg; it returns
// ok=false when everything fits in one message.
func (st *streamState) firstPiece(seg string, reserve int) (text string, used int, next string, ok bool) {
	limit := st.caps.MaxLen - reserve
	if limit <= 0 {
		limit = st.caps.MaxLen
	}
	prefix := ""
	if st.carry != "" {
		prefix = st.carry + "\n"
	}
	if ulen(st.render(prefix+seg)) <= limit {
		return "", 0, "", false
	}
	budget := limit
	for {
		chunk, n, nx := splitFirst(seg, st.carry, budget)
		out := st.render(chunk)
		if over := ulen(out) - limit; over > 0 && budget > over+16 {
			budget -= over
			continue
		}
		return out, n, nx, true
	}
}

func (st *streamState) liveText() string {
	body := st.pending(true)
	if st.carry != "" && body != "" {
		body = st.carry + "\n" + body
	}
	text := st.render(body)
	if status := st.statusLine(); status != "" {
		if strings.TrimSpace(text) != "" {
			text += "\n\n"
		}
		text += status
	}
	if strings.TrimSpace(text) == "" {
		text = placeholderText
	}
	return text
}

func (st *streamState) step(ctx context.Context) {
	if st.final {
		return
	}
	now := time.Now()
	if st.caps.Typing && (st.statusKind == "" || st.statusKind == "thinking") && !now.Before(st.nextTyping) {
		if err := st.ch.Typing(ctx, st.s.chat); err != nil {
			st.s.h.log.Debug("channel typing failed", "chat", st.s.chat.Key(), "err", err)
		}
		st.nextTyping = now.Add(st.s.h.typingEvery)
	}
	if now.Before(st.lastOp.Add(st.gap)) {
		return
	}
	if len(st.msgs) == 0 && !st.gotDelta && st.status == "" && now.Before(st.started.Add(st.s.h.placeholderIn)) {
		return
	}
	if st.liveOp(ctx) {
		st.lastOp = time.Now()
	}
}

// liveOp performs at most one platform call that moves the live view
// forward, and reports whether it made one.
func (st *streamState) liveOp(ctx context.Context) bool {
	reserve := 0
	if status := st.statusLine(); status != "" {
		reserve = ulen(status) + 2
	}
	if text, used, next, over := st.firstPiece(st.pending(true), reserve); over {
		// The live message is full: finalize it with the first piece.
		if len(st.msgs) == 0 || st.needLive {
			if !st.send(ctx, text, nil) {
				return true
			}
		} else if !st.edit(ctx, text, nil) {
			return true
		}
		st.commit += used
		st.carry = next
		st.needLive = true
		return true
	}
	text := st.liveText()
	if len(st.msgs) == 0 || st.needLive {
		if st.send(ctx, text, st.buttons()) {
			st.needLive = false
		}
		return true
	}
	if text == st.shown {
		return false
	}
	st.edit(ctx, text, st.buttons())
	return true
}

func (st *streamState) send(ctx context.Context, text string, buttons [][]core.Button) bool {
	ref, err := st.ch.Send(ctx, core.OutMessage{Chat: st.s.chat, Text: text, Buttons: buttons})
	if err != nil {
		st.s.h.log.Warn("channel stream send failed", "chat", st.s.chat.Key(), "err", err)
		return false
	}
	st.msgs = append(st.msgs, ref)
	st.shown = text
	return true
}

// edit replaces the live message; when that fails (deleted by the user,
// too old) the next op starts a fresh live message instead.
func (st *streamState) edit(ctx context.Context, text string, buttons [][]core.Button) bool {
	ref := st.msgs[len(st.msgs)-1]
	if err := st.ch.Edit(ctx, ref, core.OutMessage{Chat: st.s.chat, Text: text, Buttons: buttons}); err != nil {
		st.s.h.log.Debug("channel stream edit failed", "chat", st.s.chat.Key(), "err", err)
		st.needLive = true
		return false
	}
	st.shown = text
	return true
}

func (st *streamState) nextWake() time.Time {
	far := time.Now().Add(time.Hour)
	if st.final {
		return far
	}
	next := far
	if st.caps.Typing && (st.statusKind == "" || st.statusKind == "thinking") {
		next = earliest(next, st.nextTyping)
	}
	opAt := st.lastOp.Add(st.gap)
	if p := st.started.Add(st.s.h.placeholderIn); len(st.msgs) == 0 && !st.gotDelta && st.status == "" && p.After(opAt) {
		opAt = p
	}
	if len(st.msgs) == 0 || st.needLive || st.liveText() != st.shown {
		next = earliest(next, opAt)
	}
	return next
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// waitGap sleeps until the channel's edit interval since the last call has
// passed, so finalizing never bursts past the platform's rate limit.
func (st *streamState) waitGap(ctx context.Context) bool {
	d := time.Until(st.lastOp.Add(st.gap))
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (st *streamState) finish(ctx context.Context) {
	st.refreshTaint(ctx)
	st.releaseTail()
	raw := st.raw.String()
	if st.final && (heartbeatOK(raw) || heartbeatOK(raw[st.lastStep:])) {
		return
	}
	body := strings.TrimRightFunc(raw[st.commit:], unicode.IsSpace)
	st.defanged = st.exposed && core.NeutralizeMarkdown(raw) != raw
	if note := st.finishNote(ctx); note != "" {
		if open, _, marker := fenceState(st.carry, body); open {
			body += "\n" + marker
		}
		if strings.TrimSpace(body) != "" {
			body = strings.TrimRight(body, "\n") + "\n\n"
		}
		body += note
	}
	if header := st.scheduleHeader(ctx); header != "" && strings.TrimSpace(body) != "" {
		body = header + "\n\n" + body
	}
	var pieces []string
	for strings.TrimSpace(body) != "" {
		text, used, next, over := st.firstPiece(body, 0)
		if !over {
			prefix := ""
			if st.carry != "" {
				prefix = st.carry + "\n"
			}
			pieces = append(pieces, st.render(prefix+body))
			break
		}
		pieces = append(pieces, text)
		body, st.carry = body[used:], next
	}
	if len(pieces) == 0 {
		if len(st.msgs) > 0 && !st.needLive && !st.final {
			st.waitGap(ctx)
			st.edit(ctx, "Done.", nil)
		}
		return
	}
	for i, p := range pieces {
		if !st.waitGap(ctx) {
			return
		}
		if i == 0 && len(st.msgs) > 0 && !st.needLive && !st.final {
			if !st.edit(ctx, p, nil) {
				st.send(ctx, p, nil)
			}
		} else {
			st.send(ctx, p, nil)
		}
		st.lastOp = time.Now()
	}
}

// scheduleHeader opens a delivered schedule result with where it came from:
// nobody asked for it in this chat, so it says which schedule sent it and
// where to change it.
func (st *streamState) scheduleHeader(ctx context.Context) string {
	if st.s.mode != "final" || st.run == "" {
		return ""
	}
	run, err := st.s.h.d.Store.Runs().Get(ctx, core.SystemViewer(), st.run)
	switch {
	case err != nil:
		return ""
	case run.Trigger == core.TrigHeartbeat:
		return "_From your check-in · change it under Schedules_"
	case run.Trigger != core.TrigSchedule || run.Schedule == "":
		return ""
	}
	sc, err := st.s.h.d.Store.Schedules().Get(ctx, core.SystemViewer(), run.Schedule)
	if err != nil {
		return ""
	}
	return "_From the schedule “" + escapeMD(truncateRunes(sc.Name, 60)) + "” · change it under Schedules_"
}

// heartbeatOK reports a heartbeat reply that must not be delivered (SPEC
// §5.16); the model may say something before its final answer, so the last
// step's text counts too. Emphasis or code ticks around the token are
// tolerated, as the scheduler and the runner do.
func heartbeatOK(s string) bool {
	return strings.HasPrefix(strings.TrimLeft(strings.TrimSpace(s), "*_`> "), "HEARTBEAT_OK")
}

// finishNote is the closing line: the runner's notices when it sent any,
// else a short word on how the run ended, plus the optional usage footer.
func (st *streamState) finishNote(ctx context.Context) string {
	var lines []string
	f := st.finished
	switch {
	case len(st.notices) > 0:
		for _, n := range st.notices {
			lines = append(lines, escapeMD(n))
		}
	case st.parked:
		ttl := durationText(st.s.h.cfg().Policy.Approval.ParkTTL.D())
		lines = append(lines, "_Bothy stopped waiting. Approve within "+ttl+" to let it carry on._")
	case f != nil:
		switch f.Status {
		case core.RunStopped:
			lines = append(lines, "_Stopped._")
		case core.RunInterrupted:
			lines = append(lines, "_Interrupted._")
		case core.RunLoopDetected:
			lines = append(lines, "_Bothy stopped because it kept repeating the same step. Try asking in a different way._")
		case core.RunBudgetExceeded:
			lines = append(lines, "_"+st.s.h.budgetText(f.Budget, st.budgetLimit(ctx, f))+"_")
		case core.RunFailed:
			// The error may name secrets or internal hosts and the chat may
			// hold a guest; what happened is in the web UI's Activity.
			lines = append(lines, "_Bothy couldn't finish this task. An admin can see what happened in Activity._")
		}
	}
	if st.defanged && st.session != "" && st.s.h.firstLinkNote(st.session) {
		lines = append(lines, "_"+linkNote+"_")
	}
	if f != nil && f.Steps > 0 && st.s.h.cfg().Channels.UsageFooter {
		lines = append(lines, "_· "+usageFooter(f)+"_")
	}
	return strings.Join(lines, "\n")
}

// linkNote explains, once per conversation, why its links show as plain text
// and how to get clickable ones back without loosening the rule.
const linkNote = "Links here are shown as plain text because this chat has seen private information and content from outside. /new starts a fresh conversation with clickable links."

// firstLinkNote reports whether session s has not had linkNote yet, and
// records that it has now.
func (h *Hub) firstLinkNote(s core.SessionID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.linkNoted[s]; ok {
		return false
	}
	h.linkNoted[s] = time.Now()
	return true
}

// budgetLimit is the limit that stopped the run, from the run's limits
// snapshot; 0 when it isn't known.
func (st *streamState) budgetLimit(ctx context.Context, f *core.RunFinishedData) int64 {
	var lim core.RunLimits
	if st.run != "" {
		if run, err := st.s.h.d.Store.Runs().Get(ctx, core.SystemViewer(), st.run); err == nil {
			lim = run.Limits
		}
	}
	switch f.Budget {
	case core.BudgetSteps:
		return int64(cmp.Or(lim.MaxSteps, f.Steps))
	case core.BudgetRunCost:
		return int64(lim.MaxCost)
	}
	return 0
}

func usageFooter(f *core.RunFinishedData) string {
	steps := fmt.Sprintf("%d steps", f.Steps)
	if f.Steps == 1 {
		steps = "1 step"
	}
	return steps + " · " + moneyText(f.Cost)
}
