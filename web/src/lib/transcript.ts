import type { Action, Blob, Message, ToolCall, ToolCallStatus, ToolResult, Verdict } from './api/types.gen';

// A tool call as the run streams it (run.tool_call / tool_output / tool_result).
export interface LiveTool {
  callId: string; // tcl_<26>
  tool: string;
  title: string;
  action?: Action;
  verdict: Verdict;
  rule: string;
  out: { stream: number; chunk: string }[];
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

export type Item =
  | { kind: 'user'; key: string; msg: Message; text: string; files: Blob[]; steer: boolean }
  | { kind: 'assistant'; key: string; msg: Message; text: string; thinking: string }
  | { kind: 'notice'; key: string; msg: Message; text: string }
  | { kind: 'summary'; key: string; msg: Message; text: string }
  | { kind: 'tool'; key: string; runId?: string; call?: ToolCall; result?: ToolResult; live?: LiveTool }
  | { kind: 'stream'; key: string; text: string; thinking: string };

function text(m: Message, t = 'text'): string {
  return m.parts
    .filter((p) => p.t === t && p.text)
    .map((p) => p.text!)
    .join('\n\n');
}

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
    if (m.kind === 'notice') {
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
      if (t || files.length) items.push({ kind: 'user', key, msg: m, text: t, files, steer: m.kind === 'steer' });
      continue;
    }
    if (m.role !== 'assistant') continue;
    const t = text(m);
    const thinking = text(m, 'thinking');
    if (t || thinking) items.push({ kind: 'assistant', key, msg: m, text: t, thinking });
    const ofLive = !!live && m.run_id === live.id;
    for (const p of m.parts) {
      if (p.t !== 'tool_call' || !p.call) continue;
      let lt: LiveTool | undefined;
      if (ofLive) {
        if (skip > 0) skip--;
        else if (queue[0]?.tool === p.call.name) lt = queue.shift();
      }
      items.push({ kind: 'tool', key: `t${m.id}:${p.call.id}`, runId: m.run_id, call: p.call, result: results.get(p.call.id), live: lt ?? known?.get(p.call.id) });
    }
    if (ofLive) lastOfRun = items.length;
  }

  if (live) {
    const trailing: Item[] = queue.map((lt) => ({ kind: 'tool', key: `l${lt.callId}`, runId: live.id, live: lt }));
    items.splice(lastOfRun < 0 ? items.length : lastOfRun, 0, ...trailing);
    if (live.text || live.thinking) items.push({ kind: 'stream', key: `s${live.id}`, text: live.text, thinking: live.thinking });
  }
  return items;
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

// One-line description of a tool call, from the system-built Action when we
// have it, else from the tool name and arguments.
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
