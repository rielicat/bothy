package agent

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	msgOverhead   = 4  // estimated tokens of framing per message
	minPartTokens = 32 // hard-fit never shrinks a part below this
	truncatedNote = "[… truncated to fit the model's context window]"
)

// assembled is what a build produced for one model; the runner reads it back
// for the model the facade actually used.
type assembled struct {
	req  *core.Request
	est  int
	bd   core.Breakdown
	hash string
}

// assembler builds the cache-stable request of SPEC §5.6 for any candidate
// model; the facade calls build again on fallback, so context is re-fitted to
// that model's window.
type assembler struct {
	r        *Runner
	standing []string
	s2       string
	specs    []core.ToolSpec
	snap     profile
	hist     []histMsg
	maxOut   int

	mu    sync.Mutex
	built map[core.ModelRef]assembled
}

func (r *Runner) newAssembler(rs *runState, hist []histMsg) *assembler {
	return &assembler{
		r:        r,
		standing: r.cfg().Agent.StandingRules,
		s2:       rs.sess.SystemSnapshot,
		specs:    r.snapshotSpecs(rs),
		snap:     snapshotProfile(rs.sess.SystemSnapshot),
		hist:     hist,
		maxOut:   rs.req.MaxOutput,
		built:    map[core.ModelRef]assembled{},
	}
}

func (a *assembler) result(ref core.ModelRef) (assembled, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.built[ref]
	return v, ok
}

func (a *assembler) build(m core.ModelInfo) (*core.Request, error) {
	out := a.assemble(m)
	a.mu.Lock()
	a.built[m.Ref] = out
	a.mu.Unlock()
	return out.req, nil
}

func (a *assembler) assemble(m core.ModelInfo) assembled {
	c := a.r.cfg()
	p := profileFor(c, m.Context)
	est := func(s string) int {
		if s == "" {
			return 0
		}
		return a.r.d.LLM.Estimate(s, m.Ref)
	}
	sys1 := s1(p, a.standing)
	s2 := a.s2
	specs := a.specs
	if p != a.snap {
		specs = nil
		for _, s := range a.specs {
			if allowedIn(p, s.Def.Name) {
				specs = append(specs, s)
			}
		}
		if limit := toolCap(p); limit >= 0 && len(specs) > limit {
			specs = specs[:limit]
		}
	}
	defs := toolDefs(specs)
	toolsTok := 0
	if len(defs) > 0 {
		b, _ := json.Marshal(defs)
		toolsTok = est(string(b))
	}
	reserve := reserveFor(c, p, m.Context)
	budget := m.Context - reserve
	fixed := est(sys1) + est(s2) + toolsTok
	if budget > 0 && fixed > budget && len(defs) > 0 {
		defs, specs, toolsTok = nil, nil, 0
		fixed = est(sys1) + est(s2)
	}
	if budget > 0 && fixed > budget {
		room := max(budget-est(sys1), minPartTokens)
		s2 = headTail(s2, max(len(s2)*room/max(est(s2), 1), 64), truncatedNote)
		fixed = est(sys1) + est(s2)
	}

	hist := forModel(a.hist, m)
	toks := make([][]int, len(hist))
	msgsTok := 0
	for i, h := range hist {
		toks[i] = partTokens(est, h.msg)
		msgsTok += sum(toks[i]) + msgOverhead
	}
	if budget > 0 && fixed+msgsTok > budget {
		hist, toks = fit(est, hist, toks, budget-fixed)
	}

	msgs := make([]core.Message, len(hist))
	var bd core.Breakdown
	for i, h := range hist {
		msgs[i] = h.msg
		bd.History += msgOverhead
		for j, t := range toks[i] {
			switch h.regions[j] {
			case rToolResult:
				bd.ToolResults += t
			case rTurnContext:
				bd.TurnContext += t
			case rSummary:
				bd.Summary += t
			default:
				bd.History += t
			}
		}
	}
	bd.Constitution = est(sys1)
	bd.Persona, bd.Memory, bd.Pins, bd.Skills = splitS2(est, s2)
	bd.Tools = toolsTok
	bd.Total = bd.Constitution + bd.Persona + bd.Memory + bd.Skills + bd.Pins + bd.Tools +
		bd.Summary + bd.History + bd.ToolResults + bd.TurnContext
	bd.Window, bd.Reserve = m.Context, reserve
	bd.Cacheable = bd.Constitution + bd.Persona + bd.Memory + bd.Skills + bd.Pins + bd.Tools

	maxTokens := reserve
	if m.MaxOutput > 0 {
		maxTokens = min(maxTokens, m.MaxOutput)
	}
	if a.maxOut > 0 {
		maxTokens = min(maxTokens, a.maxOut)
	}
	system := []core.SystemBlock{{Text: sys1}, {Text: s2, CacheBreak: true}}
	req := &core.Request{
		Model:     m.Ref.Model,
		System:    system,
		Tools:     defs,
		Messages:  msgs,
		CacheTail: true,
		MaxTokens: maxTokens,
		NumCtx:    m.Context,
	}
	if len(defs) > 0 {
		req.ToolsCache, req.ToolChoice = true, "auto"
	}
	return assembled{req: req, est: bd.Total, bd: bd, hash: prefixHash(defs, system)}
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// partTokens estimates each part of a message.
func partTokens(est func(string) int, m core.Message) []int {
	out := make([]int, len(m.Parts))
	for i, p := range m.Parts {
		switch p.Type {
		case core.PartToolCall:
			if p.ToolCall != nil {
				out[i] = est(p.ToolCall.Name + string(p.ToolCall.Args))
			}
		case core.PartToolResult:
			if p.ToolResult != nil {
				out[i] = est(p.ToolResult.Content)
			}
		case core.PartImage:
			out[i] = imageTokens
		default:
			out[i] = est(p.Text)
		}
	}
	return out
}

// fit trims history to budget tokens: whole older turns go first (a turn
// keeps its tool calls with their results), then the longest parts are cut
// head and tail. The newest turn and the summary are kept as long as possible.
func fit(est func(string) int, hist []histMsg, toks [][]int, budget int) ([]histMsg, [][]int) {
	total := func() int {
		n := 0
		for _, t := range toks {
			n += sum(t) + msgOverhead
		}
		return n
	}
	hist, toks = slices.Clone(hist), slices.Clone(toks)
	last := 0
	for _, h := range hist {
		last = max(last, h.group)
	}
	for total() > budget {
		i := slices.IndexFunc(hist, func(h histMsg) bool { return h.group >= 0 && h.group < last })
		if i < 0 {
			break
		}
		g := hist[i].group
		j := i
		for j < len(hist) && hist[j].group == g {
			j++
		}
		hist = slices.Delete(hist, i, j)
		toks = slices.Delete(toks, i, j)
	}
	// Inside the newest turn, older assistant steps go next, each with its
	// results, so the turn still starts with the user's message.
	for total() > budget {
		first := slices.IndexFunc(hist, func(h histMsg) bool { return h.group == last })
		if first < 0 {
			break
		}
		i := -1
		for k := first + 1; k < len(hist); k++ {
			if hist[k].msg.Role == core.MsgAssistant && k+1 < len(hist) {
				i = k
				break
			}
		}
		if i < 0 {
			break
		}
		j := i + 1
		for j < len(hist) && hist[j].msg.Role == core.MsgTool {
			j++
		}
		if j >= len(hist) {
			break // keep the newest step: its results are what the model answers
		}
		hist = slices.Delete(hist, i, j)
		toks = slices.Delete(toks, i, j)
	}
	over := total() - budget
	if over <= 0 {
		return hist, toks
	}
	type ref struct{ i, j, t int }
	var parts []ref
	for i := range hist {
		for j, t := range toks[i] {
			if t > minPartTokens && truncatable(hist[i].msg.Parts[j]) {
				parts = append(parts, ref{i, j, t})
			}
		}
	}
	sort.SliceStable(parts, func(a, b int) bool { return parts[a].t > parts[b].t })
	for _, pr := range parts {
		if over <= 0 {
			break
		}
		want := max(pr.t-over, minPartTokens)
		msg := hist[pr.i].msg
		msg.Parts = slices.Clone(msg.Parts)
		p := msg.Parts[pr.j]
		text := partText(p)
		cut := headTail(text, max(len(text)*want/pr.t, 64), truncatedNote)
		p = withText(p, cut)
		msg.Parts[pr.j] = p
		hist[pr.i].msg = msg
		toks[pr.i] = slices.Clone(toks[pr.i])
		nt := est(cut)
		over -= pr.t - nt
		toks[pr.i][pr.j] = nt
	}
	return hist, toks
}

func truncatable(p core.Part) bool {
	switch p.Type {
	case core.PartText, core.PartThinking:
		return true
	case core.PartToolResult:
		return p.ToolResult != nil
	}
	return false
}

func partText(p core.Part) string {
	if p.Type == core.PartToolResult {
		return p.ToolResult.Content
	}
	return p.Text
}

func withText(p core.Part, s string) core.Part {
	if p.Type == core.PartToolResult {
		tr := *p.ToolResult
		tr.Content = s
		tr.Truncated = true
		p.ToolResult = &tr
		return p
	}
	p.Text = s
	return p
}

// splitS2 estimates the S2 sections for the breakdown: persona and facts,
// user profile and memory, pins, skills.
func splitS2(est func(string) int, s2 string) (persona, memory, pins, skills int) {
	section := func(tag string) string {
		open, close := "<"+tag+">", "</"+tag+">"
		i := strings.Index(s2, open)
		if i < 0 {
			return ""
		}
		j := strings.Index(s2[i:], close)
		if j < 0 {
			return s2[i:]
		}
		return s2[i : i+j+len(close)]
	}
	memory = est(section("user-profile")) + est(section("memory"))
	pins = est(section("pins"))
	skills = est(section("skills"))
	persona = max(est(s2)-memory-pins-skills, 0)
	return
}

// scaleBreakdown scales the estimated regions so they sum exactly to the
// provider-reported input (largest remainder method).
func scaleBreakdown(bd core.Breakdown, reported int) core.Breakdown {
	regions := []*int{&bd.Constitution, &bd.Persona, &bd.Memory, &bd.Skills, &bd.Pins, &bd.Tools,
		&bd.Summary, &bd.History, &bd.ToolResults, &bd.TurnContext}
	total := 0
	for _, p := range regions {
		total += *p
	}
	if reported <= 0 || total <= 0 {
		bd.Total = total
		return bd
	}
	type rem struct {
		i    int
		frac int
	}
	assigned := 0
	rems := make([]rem, len(regions))
	for i, p := range regions {
		v := *p * reported
		*p = v / total
		rems[i] = rem{i, v % total}
		assigned += *p
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := 0; assigned < reported; k++ {
		*regions[rems[k%len(rems)].i]++
		assigned++
	}
	bd.Cacheable = bd.Cacheable * reported / total
	bd.Total = reported
	return bd
}
