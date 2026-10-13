import { api, errorText } from '../../lib/api/client';
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
  RunFinishedData,
  RunQueuedData,
  RunStartedData,
  RunStatusData,
  Session,
  SessionData,
  SessionDetail,
  ToolCallData,
  ToolOutputData,
  ToolResultData,
  UsageData,
} from '../../lib/api/types.gen';
import { events } from '../../lib/sse.svelte';
import { buildItems, countToolParts, type Item, type LiveRun, type LiveTool } from '../../lib/transcript';

const MAX_TOOL_OUTPUT = 64 * 1024;

function defined<T>(xs: (T | undefined | null)[] | null | undefined): T[] {
  return (xs ?? []).filter((x): x is T => x != null);
}

export class Conversation {
  readonly id: string;
  detail: SessionDetail | null = $state(null);
  messages: Message[] = $state([]);
  hasMore = $state(false);
  loading = $state(true);
  error = $state('');
  live: LiveRun | null = $state(null);
  usage: UsageData | null = $state(null);
  preview: Breakdown | null = $state(null);
  finished: (RunFinishedData & { run_id: string }) | null = $state(null);
  approvals: Approval[] = $state([]);

  #known = new Map<string, LiveTool>();
  items: Item[] = $derived(buildItems(this.messages, this.live, this.#known));
  session: Session | null = $derived(this.detail?.session ?? null);
  pins: Pin[] = $derived(defined(this.detail?.pins));
  pending: Approval[] = $derived(this.approvals.filter((a) => a.status === 'pending' || a.status === 'parked'));

  #offs: (() => void)[] = [];
  readonly #enc: string;

  constructor(id: string) {
    this.id = id;
    this.#enc = encodeURIComponent(id);
  }

  get base() {
    return `/api/sessions/${this.#enc}`;
  }

  async load() {
    this.error = '';
    try {
      const [d, page] = await Promise.all([api.get<SessionDetail>(this.base), api.get<MessagePage>(`${this.base}/messages?limit=60`)]);
      this.detail = d;
      this.messages = defined(page.messages);
      this.hasMore = page.has_more;
      const seen = new Map(this.approvals.map((a) => [a.id, a]));
      for (const a of defined(d.pending_approvals)) seen.set(a.id, a);
      this.approvals = [...seen.values()];
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
      } else if (!d.active) this.live = null;
      if (!this.usage) this.preview = await api.get<Breakdown>(`${this.base}/context`).catch(() => null);
    } catch (e) {
      this.error = errorText(e);
    } finally {
      this.loading = false;
    }
  }

  async loadEarlier() {
    const first = this.messages[0];
    if (!first) return;
    const page = await api.get<MessagePage>(`${this.base}/messages?before=${first.seq}&limit=60`);
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
    const id = e.run_id ?? '';
    if (!this.live || this.live.id !== id) {
      this.live = { id, status: 'running', text: '', thinking: '', tools: [], skip: countToolParts(this.messages, id) };
    }
    return this.live;
  }

  listen() {
    const on = (t: EventType, fn: (e: BusEvent) => void) => events.on(t, (e) => e.session_id === this.id && fn(e));
    this.#offs = [
      on('run.queued', (e) => {
        const r = this.#run(e);
        r.status = 'queued';
        r.queued = (e.data as RunQueuedData).position;
      }),
      on('run.started', (e) => {
        this.finished = null;
        this.live = { id: e.run_id ?? '', status: 'started', model: (e.data as RunStartedData).model, text: '', thinking: '', tools: [], skip: 0 };
      }),
      on('run.status', (e) => {
        const d = e.data as RunStatusData;
        const r = this.#run(e);
        r.status = d.status;
        r.detail = d.detail;
      }),
      on('run.delta', (e) => (this.#run(e).text += (e.data as DeltaData).text)),
      on('run.thinking', (e) => (this.#run(e).thinking += (e.data as DeltaData).text)),
      on('run.tool_call', (e) => {
        const d = e.data as ToolCallData;
        const tools = this.#run(e).tools;
        const t = tools.find((x) => x.callId === d.call_id);
        if (t) Object.assign(t, { tool: d.tool, title: d.title, action: d.action, verdict: d.verdict, rule: d.rule });
        else tools.push({ callId: d.call_id, tool: d.tool, title: d.title, action: d.action, verdict: d.verdict, rule: d.rule, out: [] });
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
        this.finished = { ...(e.data as RunFinishedData), run_id: e.run_id ?? '' };
        const live = this.live;
        if (live && live.id === e.run_id) live.status = 'finished';
        void this.#refreshTail().then(() => {
          if (this.live?.id !== e.run_id) return;
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
      events.on('approval.resolved', (e) => {
        const d = e.data as ApprovalResolvedData;
        const a = this.approvals.find((x) => x.id === d.approval_id);
        if (a) this.#upsertApproval({ ...a, status: d.status, choice: d.choice, decided_by: d.by, decided_via: d.via });
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
}
