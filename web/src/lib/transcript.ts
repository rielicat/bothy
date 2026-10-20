import type { Action, Blob, Message, ToolCall, ToolCallStatus, ToolResult, Verdict } from './api/types.gen';

// A tool call as the run streams it (run.tool_call / tool_output / tool_result).
export interface LiveTool {
  callId: string; // tcl_<26>
  /** The model's id of the call, as in the persisted tool_call part. */
  providerId?: string;
  tool: string;
  title: string;
  action?: Action;
  verdict: Verdict;
  rule: string;
  out: { stream: number; chunk: string }[];
  outSize?: number;
  status?: ToolCallStatus;
  preview?: string;
  ms?: number;
  bytes?: number;
  truncated?: boolean;
  untrusted?: boolean;
}

export interface LiveRun {
  id: string;
  status: string; // run.status values plus "started"
  detail?: string;
  model?: string;
  queued?: number;
  text: string;
  thinking: string;
  tools: LiveTool[];
  // Tool-call parts of this run that were already persisted when we attached
  // mid-run; live rows match the parts after them.
  skip: number;
}

export type ToolItem = {
  kind: 'tool';
  key: string;
  runId?: string;
  /** The tool that ran: use_tool calls show the tool they reached (§7.4). */
  tool: string;
  args?: unknown;
  call?: ToolCall;
  result?: ToolResult;
  live?: LiveTool;
};

export type Item =
  /** `auto`: a schedule sent it, not the person (§7.3). */
  | { kind: 'user'; key: string; msg: Message; text: string; files: Blob[]; steer: boolean; auto?: boolean }
  | { kind: 'assistant'; key: string; msg: Message; text: string; thinking: string }
  | { kind: 'notice'; key: string; msg: Message; text: string }
  | { kind: 'summary'; key: string; msg: Message; text: string }
  | ToolItem
  | { kind: 'stream'; key: string; text: string; thinking: string };

function text(m: Message, t = 'text'): string {
  return m.parts
    .filter((p) => p.t === t && p.text)
    .map((p) => p.text!)
    .join('\n\n');
}

/** use_tool {name, args} is the tool it calls; tool_search is plumbing and never shown. */
export function unwrap(name: string, args: unknown): { tool: string; args: unknown } {
  const a = args as { name?: unknown; args?: unknown } | null;
  return name === 'use_tool' && typeof a?.name === 'string' ? { tool: a.name, args: a.args } : { tool: name, args };
}

const hidden = (tool: string) => tool === 'tool_search' || tool === 'use_tool';

/** A check-in's all-clear, with the tolerance the server has for emphasis or code ticks around it. */
export const isHeartbeatOK = (t: string) => /^[\s*_`>]*HEARTBEAT_OK/.test(t);

// Builds the transcript rows: tool results are folded into their calls, and a
// live run's streamed tool rows replace the matching persisted parts in order
// (or trail the run when their message has not been persisted yet).
// `known` keeps the streamed details of finished runs' tool calls, keyed by
// provider call id, so rows keep their verdict and timing after the run ends.
export function buildItems(messages: Message[], live: LiveRun | null, known?: Map<string, LiveTool>): Item[] {
  const results = new Map<string, ToolResult>();
  for (const m of messages) for (const p of m.parts) if (p.t === 'tool_result' && p.result) results.set(p.result.call_id, p.result);

  const queue = live ? [...live.tools] : [];
  let skip = live?.skip ?? 0;
  let lastOfRun = -1;
  const items: Item[] = [];

  for (const m of messages) {
    const key = `m${m.id}`;
    // The welcome chat's message is Bothy greeting the person (§7.3), so it reads as a reply, not a notice.
    if (m.kind === 'notice' && m.sender !== 'bothy:welcome') {
      items.push({ kind: 'notice', key, msg: m, text: text(m) });
      continue;
    }
    if (m.kind === 'summary') {
      items.push({ kind: 'summary', key, msg: m, text: text(m) });
      continue;
    }
    if (m.role === 'user') {
      const files = m.parts.filter((p) => (p.t === 'file' || p.t === 'image') && p.blob).map((p) => p.blob!);
      const t = text(m);
      if (t || files.length) items.push({ kind: 'user', key, msg: m, text: t, files, steer: m.kind === 'steer', auto: m.sender?.startsWith('schedule:') });
      continue;
    }
    if (m.role !== 'assistant') continue;
    const t = isHeartbeatOK(text(m)) ? 'Nothing needs your attention right now.' : text(m);
    const thinking = text(m, 'thinking');
    if (t || thinking) items.push({ kind: 'assistant', key, msg: m, text: t, thinking });
    const ofLive = !!live && m.run_id === live.id;
    for (const [n, p] of m.parts.entries()) {
      if (p.t !== 'tool_call' || !p.call) continue;
      const { tool, args } = unwrap(p.call.name, p.call.args);
      let lt: LiveTool | undefined;
      if (ofLive) {
        // The model's call id matches exactly; order and name are the
        // fallback for rows that don't carry it.
        const id = p.call.id;
        const exact = queue.findIndex((q) => !!q.providerId && q.providerId === id);
        if (exact >= 0) lt = queue.splice(exact, 1)[0];
        else if (skip > 0) skip--;
        else if (queue[0] && !queue[0].providerId && (queue[0].tool === tool || queue[0].tool === p.call.name)) lt = queue.shift();
      }
      if (hidden(tool)) continue;
      items.push({ kind: 'tool', key: `t${m.id}:${n}`, runId: m.run_id, tool, args, call: p.call, result: results.get(p.call.id), live: lt ?? known?.get(p.call.id) });
    }
    if (ofLive) lastOfRun = items.length;
  }

  if (live) {
    const trailing: Item[] = queue
      .map((lt) => ({ kind: 'tool' as const, key: `l${lt.callId}`, runId: live.id, tool: lt.tool === 'use_tool' && lt.action?.tool ? lt.action.tool : lt.tool, live: lt }))
      .filter((t) => !hidden(t.tool));
    items.splice(lastOfRun < 0 ? items.length : lastOfRun, 0, ...trailing);
    if (live.text || live.thinking) items.push({ kind: 'stream', key: `s${live.id}`, text: live.text, thinking: live.thinking });
  }
  return items;
}

export type Block =
  | { kind: 'day'; key: string; label: string }
  | { kind: 'steps'; key: string; runId?: string; tools: ToolItem[] }
  | Exclude<Item, ToolItem>;

/** "Today", "Yesterday", "Saturday 12 October" (with the year outside this one). */
export function dayLabel(iso: string, opts: { now?: number; locale?: string; timeZone?: string } = {}): string {
  const d = new Date(iso);
  const now = new Date(opts.now ?? Date.now());
  const day = (x: Date) => new Intl.DateTimeFormat('en-CA', { timeZone: opts.timeZone }).format(x);
  const diff = (Date.parse(day(now)) - Date.parse(day(d))) / 86_400_000;
  if (diff === 0) return 'Today';
  if (diff === 1) return 'Yesterday';
  const year = day(d).slice(0, 4) !== day(now).slice(0, 4);
  return new Intl.DateTimeFormat(opts.locale, { weekday: 'long', day: 'numeric', month: 'long', year: year ? 'numeric' : undefined, timeZone: opts.timeZone }).format(d);
}

// Consecutive steps of one run share a block (its activity group); a day
// separator goes before the first message of each calendar day.
export function buildBlocks(items: Item[], label: (iso: string) => string = (iso) => dayLabel(iso)): Block[] {
  const out: Block[] = [];
  let day = '';
  for (const it of items) {
    if (it.kind === 'tool') {
      const last = out[out.length - 1];
      if (last?.kind === 'steps' && last.runId === it.runId) last.tools.push(it);
      else out.push({ kind: 'steps', key: `g${it.key}`, runId: it.runId, tools: [it] });
      continue;
    }
    if (it.kind !== 'stream') {
      const l = label(it.msg.created_at);
      if (l !== day) out.push({ kind: 'day', key: `d${it.key}`, label: l });
      day = l;
    }
    out.push(it);
  }
  return out;
}

export function countToolParts(messages: Message[], runId: string): number {
  let n = 0;
  for (const m of messages) if (m.run_id === runId) for (const p of m.parts) if (p.t === 'tool_call') n++;
  return n;
}

const q = (s: string, max = 60) => (s.length > max ? s.slice(0, max - 1) + '…' : s);

function str(v: unknown): string {
  return typeof v === 'string' ? v : '';
}

function pathOf(url: string): string {
  try {
    return new URL(url).pathname.replace(/^\/$/, '');
  } catch {
    return '';
  }
}

// One-line description of a tool call for the v0.1 run trace, which C6
// replaces; the chat says what Bothy did with lib/activity.ts.
export function describe(tool: string, action?: Action, args?: unknown, title = ''): string {
  const a = (action?.payload ?? args ?? {}) as Record<string, unknown>;
  const argv = action?.argv?.length ? action.argv : null;
  switch (action?.kind) {
    case 'exec':
      return `Ran \`${q(argv ? (argv[0] === 'bash' && argv[1] === '-lc' ? argv[2] ?? '' : argv.join(' ')) : str(a.command))}\``;
    case 'fs.read':
      return `Read ${action.paths?.[0] ?? str(a.path)}`;
    case 'fs.write':
      return `Wrote ${action.paths?.[0] ?? str(a.path)}`;
    case 'fs.delete':
      return `Deleted ${action.paths?.[0] ?? str(a.path)}`;
    case 'http':
      return `Fetched ${q((action.host ?? '') + pathOf(action.url ?? ''), 50)}`;
    case 'net':
      return `Asked for network access to ${action.host}${action.port && action.port !== 443 ? `:${action.port}` : ''}`;
    case 'send':
      return `Sent a message${action.recipient ? ` to ${action.recipient}` : ''}`;
    case 'mcp':
      return `Used ${action.mcp_server} › ${action.mcp_tool}`;
    case 'memory':
      return 'Updated memory';
    case 'schedule':
      return 'Changed a schedule';
    case 'skill':
      return `Loaded skill ${str(a.name)}`.trim();
    case 'search':
      return `Searched the web: “${q(str(a.query))}”`;
  }
  switch (tool) {
    case 'web_search':
      return `Searched the web: “${q(str(a.query))}”`;
    case 'web_fetch':
      return `Fetched ${q(str(a.url), 50)}`;
    case 'shell':
      return `Ran \`${q(str(a.command))}\``;
    case 'python':
      return 'Ran a Python script';
    case 'fs_read':
      return `Read ${str(a.path)}`;
    case 'fs_write':
      return `Wrote ${str(a.path)}`;
    case 'fs_list':
      return `Listed ${str(a.path) || 'the workspace'}`;
    case 'fs_delete':
      return `Deleted ${str(a.path)}`;
    case 'memory':
      return 'Updated memory';
    case 'session_search':
      return `Searched past chats: “${q(str(a.query))}”`;
    case 'message':
      return 'Sent a message';
  }
  if (tool.startsWith('mcp__')) return `Used ${tool.slice(5).replace('__', ' › ')}`;
  return title || `Used ${tool}`;
}
