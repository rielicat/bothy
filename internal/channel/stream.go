package channel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	placeholderText = "Thinking…"
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
			st.status = "_Compacting context…_"
		case "tool":
			st.status = st.toolStatus
		case "thinking":
			st.status = ""
		}
	case core.RunQueuedData:
		st.status = fmt.Sprintf("_Queued (%d ahead)…_", d.Position)
	case core.DeltaData:
		if e.Type == core.EvRunDelta && d.Text != "" {
			st.appendText(d.Text)
		}
	case core.ToolCallData:
		st.releaseTail()
		st.toolStatus = "_Running " + toolLabel(d, st.s.h.redact) + "…_"
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

// toolLabel is the status-line form of a tool call: the command line for
// exec actions ("Running `pip install pandas`…"), else the tool's title. The
// command is redacted before it is shortened, since a cut secret no longer
// matches the redactor.
func toolLabel(d core.ToolCallData, redact func(string) string) string {
	if a := d.Action; a != nil && a.Kind == core.ActExec && len(a.Argv) > 0 {
		cmd := strings.Join(a.Argv, " ")
		if len(a.Argv) >= 3 && (a.Argv[1] == "-lc" || a.Argv[1] == "-c") {
			cmd = a.Argv[2]
		}
		cmd, _, _ = strings.Cut(strings.TrimSpace(redact(cmd)), "\n")
		return codeSpan(truncateRunes(cmd, 60))
	}
	title := d.Title
	if title == "" {
		title = d.Tool
	}
	return escapeMD(truncateRunes(title, 60))
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

func (st *streamState) buttons() [][]core.Button {
	if !st.caps.Buttons || st.run == "" {
		return nil
	}
	return [][]core.Button{{{Label: "Stop", Data: "s:" + string(st.run), Style: core.BtnDanger}}}
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
	if st.status != "" {
		if strings.TrimSpace(text) != "" {
			text += "\n\n"
		}
		text += st.status
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
	if st.status != "" {
		reserve = ulen(st.status) + 2
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
	if note := st.finishNote(); note != "" {
		if open, _, marker := fenceState(st.carry, body); open {
			body += "\n" + marker
		}
		if strings.TrimSpace(body) != "" {
			body = strings.TrimRight(body, "\n") + "\n\n"
		}
		body += note
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

// heartbeatOK reports a heartbeat reply that must not be delivered (SPEC
// §5.16); the model may say something before its final answer, so the last
// step's text counts too. Emphasis or code ticks around the token are
// tolerated, as the scheduler and the runner do.
func heartbeatOK(s string) bool {
	return strings.HasPrefix(strings.TrimLeft(strings.TrimSpace(s), "*_`> "), "HEARTBEAT_OK")
}

// finishNote is the closing line: the runner's notices when it sent any,
// else a short word on how the run ended, plus the optional usage footer.
func (st *streamState) finishNote() string {
	var lines []string
	f := st.finished
	switch {
	case len(st.notices) > 0:
		for _, n := range st.notices {
			lines = append(lines, escapeMD(n))
		}
	case st.parked:
		lines = append(lines, "_Parked: waiting for approval. Approve any time in the next 24 h._")
	case f != nil:
		errText := strings.TrimSpace(st.s.h.redact(f.Error))
		switch f.Status {
		case core.RunStopped:
			lines = append(lines, "_Stopped._")
		case core.RunInterrupted:
			lines = append(lines, "_Interrupted._")
		case core.RunLoopDetected:
			lines = append(lines, "_Stopped: I kept repeating myself. Try rephrasing the request._")
		case core.RunBudgetExceeded:
			lines = append(lines, "Budget exceeded"+suffix(errText))
		case core.RunFailed:
			// The error may name secrets or internal hosts and the chat may
			// hold a guest; what happened is in the web UI's Activity.
			lines = append(lines, "_Something went wrong, so I couldn't finish. An admin can see what happened in Activity._")
		}
	}
	if f != nil && f.Steps > 0 && st.s.h.cfg().Channels.UsageFooter {
		lines = append(lines, "_· "+usageFooter(f)+"_")
	}
	return strings.Join(lines, "\n")
}

func suffix(errText string) string {
	if errText == "" {
		return "."
	}
	return ": " + escapeMD(truncateRunes(errText, 300))
}

func usageFooter(f *core.RunFinishedData) string {
	steps := fmt.Sprintf("%d steps", f.Steps)
	if f.Steps == 1 {
		steps = "1 step"
	}
	return steps + " · " + tokenCount(f.Usage.TotalInput()+f.Usage.Output) + " tok · " + costText(f.Cost)
}

func tokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func costText(m core.Micros) string {
	if m.USD() < 0.1 {
		return fmt.Sprintf("$%.3f", m.USD())
	}
	return fmt.Sprintf("$%.2f", m.USD())
}
