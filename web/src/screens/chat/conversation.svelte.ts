import { untrack } from 'svelte';
import { api, ApiError, errorText } from '../../lib/api/client';
import type {
  Approval,
  ApprovalData,
  ApprovalResolvedData,
  Breakdown,
  DeltaData,
  Event as BusEvent,
  EventType,
  Message,
  MessageData,
  MessagePage,
  Pin,
  RunDetail,
  RunFinishedData,
  RunQueuedData,
  RunStartedData,
  RunStatusData,
  Session,
  SessionData,
  SessionDetail,
  ToolCallData,
  ToolCallRecord,
  ToolOutputData,
  ToolResultData,
  UsageData,
} from '../../lib/api/types.gen';
import { events } from '../../lib/sse.svelte';
import { buildItems, countToolParts, type Item, type LiveRun, type LiveTool } from '../../lib/transcript';

const MAX_TOOL_OUTPUT = 64 * 1024;
// Run details are fetched for the most recent runs when a chat opens, so
// decided approvals and how each step was allowed survive a reload.
const RECENT_RUNS = 8;

function defined<T>(xs: (T | undefined | null)[] | null | undefined): T[] {
  return (xs ?? []).filter((x): x is T => x != null);
}

/** How the latest run ended, from its event or, after a reload, its stored record. */
export interface Outcome extends Partial<RunFinishedData> {
  run_id: string;
  status: string;
}

export class Conversation {
  id = $state('');
  detail: SessionDetail | null = $state(null);
  messages: Message[] = $state([]);
  hasMore = $state(false);
  loading = $state(true);
  error = $state('');
  /** The server knows no such chat for this viewer (404), which no retry changes. */
  missing = $state(false);
  live: LiveRun | null = $state(null);
  usage: UsageData | null = $state(null);
  preview: Breakdown | null = $state(null);
  finished: Outcome | null = $state(null);
  approvals: Approval[] = $state([]);
  runs: Record<string, RunDetail> = $state({});
  /** Messages sent while Bothy was busy, by message text, until the reply starts. */
  queued: string[] = $state([]);

  #known = new Map<string, LiveTool>();
  items: Item[] = $derived(buildItems(this.messages, this.live, this.#known));
  session: Session | null = $derived(this.detail?.session ?? null);
  pins: Pin[] = $derived(defined(this.detail?.pins));
  running = $derived(!!this.live && this.live.status !== 'finished');
  lastRunId = $derived(this.live?.id || this.finished?.run_id || ([...this.messages].reverse().find((m) => m.run_id)?.run_id ?? ''));

  /** Approvals by tool call id (tcl_…): events and the pending list first, then stored runs. */
  byCall = $derived.by(() => {
    const out = new Map<string, Approval>();
    for (const d of Object.values(this.runs)) for (const a of defined(d.approvals)) out.set(a.tool_call_id, a);
    for (const a of this.approvals) {
      // An event's copy is newer but may lack the names a stored read fills
      // in (ballots, decider), which a two-person decision needs.
      const stored = out.get(a.tool_call_id);
      const ballots = (x?: Approval) => x?.approvals?.votes?.length ?? x?.votes?.length ?? 0;
      const fuller = stored && ballots(stored) > ballots(a) ? stored : a;
      out.set(
        a.tool_call_id,
        stored ? { ...stored, ...a, approvals: fuller.approvals ?? stored.approvals, votes: fuller.votes ?? stored.votes, decided_by_name: a.decided_by_name || stored.decided_by_name } : a,
      );
    }
    return out;
  });

  /** Stored tool calls by run and provider call id. */
  records = $derived.by(() => {
    const out = new Map<string, ToolCallRecord>();
    for (const d of Object.values(this.runs)) for (const t of defined(d.tool_calls)) out.set(`${t.run_id}:${t.call_id}`, t);
    return out;
  });

  outcome: Outcome | null = $derived.by(() => {
    const id = this.lastRunId;
    if (!id || this.running) return null;
    if (this.finished?.run_id === id) return this.finished;
    const r = this.runs[id]?.run;
    if (!r) return null;
    // The stored run doesn't say which limit stopped it; its limits and totals do.
    const l = r.limits;
    const budget = r.status !== 'budget_exceeded' ? undefined : l.max_steps && r.steps >= l.max_steps ? 'steps' : l.max_cost_micros && r.cost_micros >= l.max_cost_micros ? 'run_cost' : 'daily_cost';
    return { run_id: id, status: r.status, error: r.error, steps: r.steps, budget };
  });

  #offs: (() => void)[] = [];
  // Counts run events, so a load that raced a run starting keeps the live run.
  #seen = 0;
  #fetching = new Map<string, Promise<RunDetail | null>>();

  constructor(id: string) {
    this.id = id;
    if (!id) this.loading = false;
  }

  get base() {
    return `/api/sessions/${encodeURIComponent(this.id)}`;
  }

  /** A new chat got its session on the first send; events for it are already being followed. */
  adopt(s: Session) {
    this.id = s.id;
    this.detail = { session: s, pins: [], pending_approvals: [], refine: '' };
  }

  // Also the resync handler: what the server says is pending replaces the
  // local list, so a card resolved while events were missed loses its
  // buttons. Only cards that arrived by event during the fetch are kept,
  // since the snapshot may predate them.
  async load() {
    if (!this.id) return;
    this.error = '';
    // untracked: an effect calls load(), and load() rewrites this list.
    const before = untrack(() => new Set(this.approvals.map((a) => a.id)));
    const seen = this.#seen;
    try {
      const [d, page] = await Promise.all([api.get<SessionDetail>(this.base), api.get<MessagePage>(`${this.base}/messages?limit=60`)]);
      this.detail = d;
      this.messages = defined(page.messages);
      this.hasMore = page.has_more;
      const pending = defined(d.pending_approvals);
      const listed = new Set(pending.map((a) => a.id));
      // Decided cards stay decided; a pending one not listed any more was decided elsewhere.
      this.approvals = [...pending, ...this.approvals.filter((a) => !listed.has(a.id) && (!before.has(a.id) || (a.status !== 'pending' && a.status !== 'parked')))];
      if (d.active && this.live?.id !== d.active.run_id) {
        this.live = {
          id: d.active.run_id,
          status: d.active.status,
          queued: d.active.queued,
          text: '',
          thinking: '',
          tools: [],
          skip: countToolParts(this.messages, d.active.run_id),
        };
      } else if (!d.active && seen === this.#seen) this.live = null;
      this.#recentRuns();
      if (!this.usage) this.preview = await api.get<Breakdown>(`${this.base}/context`).catch(() => null);
    } catch (e) {
      this.error = errorText(e);
      this.missing = e instanceof ApiError && e.status === 404;
    } finally {
      this.loading = false;
    }
  }

  #recentRuns() {
    const ids: string[] = [];
    for (const m of [...this.messages].reverse()) {
      if (!m.run_id || ids.includes(m.run_id)) continue;
      if (m.parts.some((p) => p.t === 'tool_call') || ids.length === 0) ids.push(m.run_id);
      if (ids.length >= RECENT_RUNS) break;
    }
    for (const id of ids) void this.loadRun(id, true);
  }

  /** Fetches a run's stored record once (again with `fresh`, after it finishes). */
  loadRun(id: string, fresh = false): Promise<RunDetail | null> {
    let p = fresh ? undefined : this.#fetching.get(id);
    if (!p) {
      p = api
        .get<RunDetail>(`/api/runs/${encodeURIComponent(id)}`)
        .then((d) => (this.runs[id] = d))
        .catch(() => {
          this.#fetching.delete(id);
          return null;
        });
      this.#fetching.set(id, p);
    }
    return p;
  }

  async loadEarlier() {
    const first = this.messages[0];
    if (!first) return;
    await this.#earlier(first.seq, 60);
  }

  // Pages back until message seq is loaded (a search hit), in pages large
  // enough to get there in one request when the server allows it.
  async loadUntil(seq: number) {
    for (let first = this.messages[0]; first && first.seq > seq && this.hasMore; first = this.messages[0]) {
      await this.#earlier(first.seq, Math.min(500, Math.max(60, first.seq - seq + 10)));
      if (this.messages[0]?.seq === first.seq) return;
    }
  }

  async #earlier(before: number, limit: number) {
    const page = await api.get<MessagePage>(`${this.base}/messages?before=${before}&limit=${limit}`);
    const have = new Set(this.messages.map((m) => m.id));
    this.messages = [...defined(page.messages).filter((m) => !have.has(m.id)), ...this.messages];
    this.hasMore = page.has_more;
  }

  async #refreshTail() {
    try {
      const page = await api.get<MessagePage>(`${this.base}/messages?limit=60`);
      for (const m of defined(page.messages)) this.upsert(m);
    } catch {
      // the next event or resync catches up
    }
  }

  upsert(m: Message) {
    const i = this.messages.findIndex((x) => x.id === m.id);
    if (i >= 0) {
      this.messages[i] = m;
      return;
    }
    let at = this.messages.length;
    while (at > 0 && this.messages[at - 1].seq > m.seq) at--;
    this.messages.splice(at, 0, m);
  }

  #upsertApproval(a: Approval) {
    const i = this.approvals.findIndex((x) => x.id === a.id);
    if (i >= 0) this.approvals[i] = a;
    else this.approvals.push(a);
  }

  #run(e: BusEvent): LiveRun {
    this.#seen++;
    const id = e.run_id ?? '';
    if (!this.live || this.live.id !== id) {
      this.live = { id, status: 'running', text: '', thinking: '', tools: [], skip: countToolParts(this.messages, id) };
    }
    return this.live;
  }

  listen() {
    const on = (t: EventType, fn: (e: BusEvent) => void) => events.on(t, (e) => !!this.id && e.session_id === this.id && fn(e));
    this.#offs = [
      on('run.queued', (e) => {
        const r = this.#run(e);
        r.status = 'queued';
        r.queued = (e.data as RunQueuedData).position;
      }),
      on('run.started', (e) => {
        this.#seen++;
        this.finished = null;
        this.queued = [];
        this.live = { id: e.run_id ?? '', status: 'started', model: (e.data as RunStartedData).model, text: '', thinking: '', tools: [], skip: 0 };
      }),
      on('run.status', (e) => {
        const d = e.data as RunStatusData;
        const r = this.#run(e);
        r.status = d.status;
        r.detail = d.detail;
      }),
      on('run.delta', (e) => {
        this.queued = [];
        this.#run(e).text += (e.data as DeltaData).text;
      }),
      on('run.thinking', (e) => (this.#run(e).thinking += (e.data as DeltaData).text)),
      on('run.tool_call', (e) => {
        const d = e.data as ToolCallData;
        const tools = this.#run(e).tools;
        const t = tools.find((x) => x.callId === d.call_id);
        if (t) Object.assign(t, { tool: d.tool, title: d.title, action: d.action, verdict: d.verdict, rule: d.rule, providerId: d.provider_call_id });
        else tools.push({ callId: d.call_id, providerId: d.provider_call_id, tool: d.tool, title: d.title, action: d.action, verdict: d.verdict, rule: d.rule, out: [] });
      }),
      on('run.tool_output', (e) => {
        const d = e.data as ToolOutputData;
        const t = this.#run(e).tools.find((x) => x.callId === d.call_id);
        if (!t) return;
        const size = t.outSize ?? 0;
        if (size >= MAX_TOOL_OUTPUT) return;
        const chunk = d.chunk.slice(0, MAX_TOOL_OUTPUT - size);
        t.out.push({ stream: d.stream, chunk });
        t.outSize = size + chunk.length;
      }),
      on('run.tool_result', (e) => {
        const d = e.data as ToolResultData;
        const t = this.#run(e).tools.find((x) => x.callId === d.call_id);
        if (t) Object.assign(t, { status: d.status, preview: d.preview, ms: d.ms, bytes: d.bytes, truncated: d.truncated, untrusted: d.untrusted });
      }),
      on('run.usage', (e) => (this.usage = e.data as UsageData)),
      on('run.parked', (e) => (this.#run(e).status = 'parked')),
      on('run.finished', (e) => {
        const id = e.run_id ?? '';
        this.finished = { ...(e.data as RunFinishedData), run_id: id };
        const live = this.live;
        if (live && live.id === id) live.status = 'finished';
        void this.loadRun(id, true);
        void this.#refreshTail().then(() => {
          if (this.live?.id !== id) return;
          for (const it of this.items) if (it.kind === 'tool' && it.call && it.live) this.#known.set(it.call.id, $state.snapshot(it.live) as LiveTool);
          this.live = null;
        });
      }),
      on('message.created', (e) => {
        const m = (e.data as MessageData).message;
        if (!m) return;
        this.upsert(m);
        if (m.role === 'assistant' && this.live && m.run_id === this.live.id) {
          this.live.text = '';
          this.live.thinking = '';
        }
      }),
      on('session.updated', (e) => {
        const d = e.data as SessionData;
        if (this.detail) this.detail.session = { ...this.detail.session, title: d.title, taint: d.taint, epoch: d.epoch };
      }),
      on('approval.requested', (e) => {
        const a = (e.data as ApprovalData).approval;
        if (a) this.#upsertApproval(a);
      }),
      on('approval.voted', (e) => {
        const a = (e.data as ApprovalData).approval;
        if (a) this.#upsertApproval(a);
      }),
      events.on('approval.resolved', (e) => {
        const d = e.data as ApprovalResolvedData;
        const a = this.byCall.get([...this.byCall].find(([, x]) => x.id === d.approval_id)?.[0] ?? '');
        if (a) this.#upsertApproval({ ...a, status: d.status, choice: d.choice, decided_by: d.by, decided_via: d.via, decided_at: e.at });
      }),
      events.onResync(() => void this.load()),
    ];
  }

  dispose() {
    this.#offs.forEach((f) => f());
    this.#offs = [];
  }

  resolved(a: Approval) {
    this.#upsertApproval(a);
  }

  setSession(s: Session) {
    if (this.detail) this.detail.session = s;
  }

  setPins(pins: Pin[]) {
    if (this.detail) this.detail.pins = pins;
  }

  async addPin(text: string) {
    const p = await api.post<Pin>(`${this.base}/pins`, { text });
    this.setPins([...this.pins, p]);
  }
}
