package core

// Refine mode (v0.2 F3): a final answer is drafted N times and each draft is
// taken through D rounds of continue → critique → correct before a plurality
// vote over the normalized final answers picks the reply.

// EvRunRefine reports refinement progress (bus only; sinks never get it).
const EvRunRefine EventType = "run.refine"

// RefineStatusDetail is the run.status detail (status "thinking") while a
// final answer is being refined; sinks see it, since run.refine is bus only.
const RefineStatusDetail = "Refining the answer"

// PartRefine is the trace of the refinement that produced an assistant
// message. It is stored with the message and never sent to a model.
const PartRefine PartType = "refine"

type RefineRole string

const (
	RefineDraft    RefineRole = "draft"
	RefineContinue RefineRole = "continue"
	RefineCritique RefineRole = "critique"
	RefineCorrect  RefineRole = "correct"
)

// A session's own refine setting is the meta value RefineMetaKey(session):
// RefineOn or RefineOff. Without one the session follows agent.refine.enabled.
const (
	RefineOn  = "on"
	RefineOff = "off"
)

func RefineMetaKey(s SessionID) string { return "agent.refine." + string(s) }

// RefineEntry is one model call of a refinement.
type RefineEntry struct {
	Draft     int        `json:"draft"` // 0-based; draft 0 is the run's own answer step
	Round     int        `json:"round"` // 0 for drafts, 1..depth for the rounds
	Role      RefineRole `json:"role"`
	Text      string     `json:"text"`             // redacted; long texts keep their head and tail
	Answer    string     `json:"answer,omitempty"` // normalized final answer (draft, continue, correct)
	Confirmed bool       `json:"confirmed,omitempty"`
	Abridged  bool       `json:"abridged,omitempty"` // the quoted draft was shortened to fit the model's window
	CallID    string     `json:"call_id,omitempty"`  // llm_calls id
	Model     string     `json:"model,omitempty"`
	Cost      Micros     `json:"cost_micros"`
	Error     string     `json:"error,omitempty"`
}

type RefineVote struct {
	Answer string `json:"answer"` // normalized
	Count  int    `json:"count"`
	Drafts []int  `json:"drafts"` // ascending
}

type RefineTrace struct {
	Drafts    int  `json:"drafts"` // configured breadth N
	Depth     int  `json:"depth"`  // configured depth D
	Rounds    int  `json:"rounds"` // rounds completed
	EarlyStop bool `json:"early_stop"`
	// Fallback says why refinement ended before its last round, showing the
	// best answer so far: budget | time | new_message | stopped | failed.
	Fallback string        `json:"fallback,omitempty"`
	Winner   int           `json:"winner"`           // draft shown as the reply
	Answer   string        `json:"answer,omitempty"` // the winner's normalized final answer
	Votes    []RefineVote  `json:"votes"`            // most votes first, ties by earliest draft
	Entries  []RefineEntry `json:"entries,omitempty"`
	Calls    int           `json:"calls"` // model calls made for this answer, the first draft included
	Cost     Micros        `json:"cost_micros"`
}

// RefineData is the payload of run.refine: phase "start" (Drafts, Depth,
// MaxCalls), "entry" (one finished call) and "done" (the trace without its
// entries; the persisted message carries all of it).
type RefineData struct {
	Phase    string       `json:"phase"`
	Drafts   int          `json:"drafts,omitempty"`
	Depth    int          `json:"depth,omitempty"`
	MaxCalls int          `json:"max_calls,omitempty"`
	Entry    *RefineEntry `json:"entry,omitempty"`
	Trace    *RefineTrace `json:"trace,omitempty"`
}

func (RefineData) eventData() {}

// RefineMaxCalls bounds the model calls of one refined answer: every draft
// makes one call and up to three per round.
func RefineMaxCalls(drafts, depth int) int { return drafts * (1 + 3*depth) }
