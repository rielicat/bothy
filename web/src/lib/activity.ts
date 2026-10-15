// What Bothy does, said in words (docs/design/ui.md §7.4): one phrase per tense
// for every tool, the state a step is in, and the summary line a finished group
// of steps folds into. Paths, hosts, branches and commands stay verbatim and
// render in mono ('code' segments); names people chose ('name' segments) stay
// verbatim in the body font.
import type { Action, ApprovalStatus, ToolCallStatus } from './api/types.gen';
import { serviceName } from './format';

export type SegKind = 'text' | 'code' | 'name';
export interface Seg {
  k: SegKind;
  v: string;
}
export type Phrase = Seg[];
export type Tense = 'ask' | 'ing' | 'doing' | 'done' | 'failed';
export type Phrases = Record<Tense, Phrase> & { family: string };
export type StepState = 'running' | 'waiting' | 'done' | 'failed' | 'denied' | 'expired' | 'blocked';

export interface StepInput {
  tool: string;
  action?: Action;
  args?: unknown;
  /** The MCP title annotation, when the server sends one. */
  title?: string;
  /** The tool's result, for the numbers only the result knows (a pull request's number). */
  result?: string;
}

// ask · -ing · doing · done · failed. An empty doing or failed falls back to
// "{-ing}…" and "Couldn't {ask}". [ … ] is dropped when a value inside it is empty.
// "+3 −1" keeps a no-break space so the counts never split across lines.
const T: Record<string, string[]> = {
  fs_read: ['read {path}', 'reading {path}', 'Reading {path}…', 'Read {path}', "Couldn't read {path}"],
  fs_list: ['look in {dir}', 'looking in {dir}', 'Looking in {dir}…', 'Looked in {dir}', "Couldn't open {dir}"],
  'fs_write:create': ['create {path}', 'creating {path}', 'Saving {path}…', 'Created {path}', "Couldn't save {path}"],
  'fs_write:overwrite': ['change {path}', 'changing {path}', 'Saving {path}…', 'Changed {path}[ (+{a}\u00a0−{r})]', "Couldn't save {path}"],
  'fs_write:append': ['add to {path}', 'adding to {path}', 'Saving {path}…', 'Added to {path}', "Couldn't save {path}"],
  fs_delete: ['delete {path}', 'deleting {path}', 'Deleting {path}…', 'Deleted {path}', "Couldn't delete {path}"],
  shell: ['run {command}', 'running {command}', 'Running {command}…', 'Ran {command}', '{command} finished with errors'],
  python: ['run a Python script', 'running a Python script', 'Running a Python script…', 'Ran a Python script', 'The script stopped with an error'],
  web_search: ['search the web', 'searching the web', 'Searching the web[ for “{q}”]…', 'Searched the web[ for “{q}”]', "Web search isn't working right now"],
  'web_fetch:get': ['read {host}', 'reading {host}', 'Opening {host}…', 'Read {hostpath}', "Couldn't open {host}"],
  'web_fetch:send': ['send data to {host}', 'sending data to {host}', 'Sending data to {host}…', 'Sent data to {host}', "Couldn't reach {host}"],
  net_allow: ['reach {host}', 'letting Bothy reach {host}', '', 'Can now reach {host}', ''],
  memory: ['remember something', 'remembering {note}', '', 'Remembered {note}', "Couldn't save that"],
  'memory:replace': ['update a saved note', 'updating a saved note to {note}', '', 'Updated a saved note to {note}', "Couldn't update that note"],
  'memory:remove': ['forget something', 'forgetting {note}', '', 'Forgot {note}', "Couldn't remove that note"],
  session_search: ['search past chats', 'searching past chats', 'Searching past chats[ for “{q}”]…', 'Searched past chats[ for “{q}”]', ''],
  message: ['send a message to {chat}', 'sending a message to {chat}', 'Sending…', 'Sent a message to {chat}', "Couldn't send the message"],
  skill_load: ['follow the “{skill}” skill', 'following “{skill}”', '', 'Followed the “{skill}” skill', ''],
  schedule_list: ['check the schedules', 'checking the schedules', '', 'Checked the schedules', ''],
  schedule_create: ['create the schedule “{name}”', 'creating the schedule “{name}”', '', 'Created the schedule “{name}”', "Couldn't create the schedule"],
  schedule_delete: ['delete the schedule “{name}”', 'deleting the schedule “{name}”', '', 'Deleted the schedule “{name}”', "Couldn't delete the schedule"],
  repo_list: ['check the connected repositories', 'checking repositories', '', 'Checked the connected repositories', ''],
  repo_checkout: ['get a copy of {repo}', 'getting a copy of {repo}', 'Getting a copy of {repo}[ ({ref})]…', 'Got a copy of {repo}[ ({ref})]', "Couldn't get {repo}"],
  repo_changes: ['review changes in {repo}', 'reviewing changes', '', 'Reviewed changes in {repo}[ ({files})]', ''],
  repo_open_pr: ['open a pull request in {repo}', 'opening a pull request in {repo}', 'Opening a pull request…', 'Opened pull request[ #{n}] in {repo}', "Couldn't open the pull request"],
  github_issues: ['read issues in {repo}', 'reading issues', '', 'Read issues in {repo}', "Couldn't read issues in {repo}"],
  github_pr: ['read pull request #{n}', 'reading #{n}', '', 'Read pull request #{n} in {repo}', "Couldn't read pull request #{n}"],
  'github_pr:list': ['list pull requests in {repo}', 'listing pull requests', '', 'Listed pull requests in {repo}', "Couldn't list pull requests in {repo}"],
  github_comment: ['comment on #{n} in {repo}', 'commenting on #{n} in {repo}', '', 'Commented on #{n} in {repo}', "Couldn't post the comment"],
  github_runs: ['check builds in {repo}', 'checking builds', '', 'Checked recent builds in {repo}', ''],
  // The tool only starts the workflow; the outcome is posted to the chat when the run ends.
  release_deploy: ['deploy {repo} to {env}', 'deploying {repo} to {env}', 'Deploying {repo} to {env}…', 'Started deploying {repo} to {env}', 'Deploy to {env} failed'],
  release_status: ['check the {env} deploy', 'checking {env}', '', 'Checked the {env} deploy', ''],
  knowledge_search: ['search project knowledge', 'searching project knowledge', '', 'Searched project knowledge[ for “{q}”]', ''],
  notes: ['update the notes for {repo}', 'updating the notes for {repo}', '', 'Updated the notes for {repo}', ''],
  mcp: ['use {server}: {title}', 'using {server}: {title}', 'Using {server}: {title}…', 'Used {server}: {title}', '{server}: {title} failed'],
  other: ['use {tool}', 'using {tool}', 'Using {tool}…', 'Used {tool}', '{tool} failed'],
};
T.skill_read = T.skill_load;

/** Every tool the phrase table covers, for tests and the gallery. */
export const knownTools = Object.keys(T);

const CODE = new Set(['path', 'dir', 'command', 'host', 'hostpath', 'ref']);

/** "create_issue" → "Create issue". */
export function humanise(s: string): string {
  const t = s.replace(/[_-]+/g, ' ').trim();
  return t.charAt(0).toUpperCase() + t.slice(1);
}

export function shellCommand(action?: Action, args?: Record<string, unknown>): string {
  const argv = action?.argv;
  if (argv?.length) return argv[0] === 'bash' && argv[1] === '-lc' ? (argv[2] ?? '') : argv.join(' ');
  return typeof args?.command === 'string' ? args.command : '';
}

/** A command as a step's line shows it: no leading cd, cut at its first
 * separator and at 60 characters. Details keep the whole command. */
export function shortCommand(cmd: string): string {
  let c = cmd.trim().replace(/^(cd\s+\S+\s*&&\s*)+/, '');
  if (c.length <= 60) return c;
  let quote = '';
  for (let i = 1; i < c.length; i++) {
    const ch = c[i];
    if (quote) {
      if (ch === quote) quote = '';
    } else if (ch === '"' || ch === "'") {
      quote = ch;
    } else if (ch === ';' || ch === '|' || (ch === '&' && c[i - 1] !== '>' && c[i + 1] !== '>')) {
      c = c.slice(0, i).trimEnd() + ' …';
      break;
    }
  }
  return c.length <= 62 ? c : c.slice(0, 59).trimEnd() + '…';
}

/** "+3 −1" counts of a unified diff. */
export function diffCounts(diff = ''): { a: number; r: number } {
  let a = 0;
  let r = 0;
  for (const l of diff.split('\n')) {
    if (l.startsWith('+') && !l.startsWith('+++')) a++;
    else if (l.startsWith('-') && !l.startsWith('---')) r++;
  }
  return { a, r };
}

// What only the result knows, read from the words Bothy's own tools write:
// "Opened pull request #482: https://…", "Commented on acme/web#482: https://…",
// "… Run 1003: https://…/actions/runs/1003. The outcome will be posted…" and
// repo_changes' "3 changed files:" on its second line. Other tools' results
// hold whatever a page or a command printed, so they never become a link.
function fromResult(tool: string, s = ''): { n: string; files: number; link: string } {
  let n = '';
  let link = '';
  let files = 0;
  if (tool === 'repo_open_pr') {
    const m = /^Opened pull request #(\d+): (\S+)/.exec(s);
    if (m) [, n, link] = m;
  } else if (tool === 'github_comment') {
    link = /^Commented on \S+#\d+: (\S+)/.exec(s)?.[1] ?? '';
  } else if (tool === 'release_deploy') {
    // The last one: the approvers' names come earlier in the sentence.
    link = [...s.matchAll(/\. Run \d+: (\S+?)\.(?=\s|$)/g)].pop()?.[1] ?? '';
  } else if (tool === 'repo_changes') {
    files = Number(/^[^\n]*\n(\d+) changed files?:/.exec(s)?.[1] ?? 0);
  }
  return { n, files, link: /^https:\/\//.test(link) ? link : '' };
}

/** A link the step's result points at (an opened pull request, a comment, a deploy run), for "↗". */
export function stepLink(s: StepInput): string {
  return fromResult(s.tool, s.result).link;
}

function keyOf(s: StepInput, a: Record<string, unknown>): string {
  const t = s.tool;
  if (t === 'fs_write') {
    const mode = String(a.mode ?? '');
    if (mode === 'append') return 'fs_write:append';
    return mode === 'create' || /^--- \/dev\/null/m.test(s.action?.diff ?? '') ? 'fs_write:create' : 'fs_write:overwrite';
  }
  if (t === 'web_fetch') {
    const method = String(s.action?.method ?? a.method ?? 'GET').toUpperCase();
    return method === 'GET' || method === 'HEAD' ? 'web_fetch:get' : 'web_fetch:send';
  }
  if (t === 'memory' && (a.op === 'replace' || a.op === 'remove')) return `memory:${a.op}`;
  // Without a number github_pr lists the repository's pull requests.
  if (t === 'github_pr' && a.number == null && a.pr == null) return 'github_pr:list';
  if (t.startsWith('mcp__') || s.action?.kind === 'mcp') return 'mcp';
  return t in T ? t : 'other';
}

function values(s: StepInput, a: Record<string, unknown>): Record<string, string> {
  const str = (...ks: string[]) => {
    for (const k of ks) {
      const v = a[k];
      if (typeof v === 'string' && v) return v;
      if (typeof v === 'number') return String(v);
    }
    return '';
  };
  const act = s.action;
  const path = act?.paths?.[0] ?? str('path');
  const url = act?.url ?? str('url');
  let host = act?.host ?? '';
  let tail = '';
  try {
    const u = new URL(url);
    host ||= u.hostname;
    tail = u.pathname === '/' ? '' : u.pathname;
  } catch {
    // no URL: the host alone
  }
  // The memory tool sends the entry as new (add, replace) or old (remove).
  const note = str('new', 'old', 'text', 'content', 'entry', 'note');
  const { a: added, r: removed } = diffCounts(act?.diff);
  const r = fromResult(s.tool, s.result);
  const [server, ...rest] = (act?.mcp_server ? `${act.mcp_server}__${act.mcp_tool ?? ''}` : s.tool.replace(/^mcp__/, '')).split('__');
  return {
    path,
    dir: path || str('dir'),
    command: shortCommand(shellCommand(act, a)),
    host,
    hostpath: host + tail,
    q: str('query', 'q'),
    note: note ? `“${note.length > 60 ? note.slice(0, 59) + '…' : note}”` : 'something',
    chat: act?.recipient || str('to', 'chat') || 'this chat',
    skill: str('name', 'skill'),
    name: str('name', 'title'),
    repo: str('repo', 'repository') || [str('owner'), str('name')].filter(Boolean).join('/'),
    ref: str('ref', 'branch'),
    files: r.files ? `${r.files} ${r.files === 1 ? 'file' : 'files'}` : '',
    n: str('number', 'pr', 'issue') || r.n,
    env: str('environment', 'env'),
    a: act?.diff ? String(added) : '',
    r: act?.diff ? String(removed) : '',
    server: serviceName(server ?? ''),
    title: mcpTitle(s.title) || humanise(rest.join('__')),
    tool: s.tool,
  };
}

/** Fills a template into segments; `{dir}` with no folder reads "Bothy's files". */
export function fill(tpl: string, vals: Record<string, string>): Phrase {
  const t = tpl.replace(/\[([^\]]*)\]/g, (_, inner: string) => ([...inner.matchAll(/\{(\w+)\}/g)].every((m) => vals[m[1]]) ? inner : ''));
  const out: Phrase = [];
  const push = (k: SegKind, v: string) => {
    if (!v) return;
    const last = out[out.length - 1];
    if (last && last.k === 'text' && k === 'text') last.v += v;
    else out.push({ k, v });
  };
  t.split(/(\{\w+\})/).forEach((part) => {
    const m = /^\{(\w+)\}$/.exec(part);
    if (!m) return push('text', part);
    const v = vals[m[1]] ?? '';
    if (m[1] === 'dir' && (!v || v === '.')) return push('text', "Bothy's files");
    push(CODE.has(m[1]) ? 'code' : 'name', v);
  });
  return out;
}

const cap = (p: Phrase): Phrase => (p[0]?.k === 'text' ? [{ k: 'text', v: p[0].v.charAt(0).toUpperCase() + p[0].v.slice(1) }, ...p.slice(1)] : p);

export function phrases(s: StepInput): Phrases {
  const a = (s.action?.payload ?? s.args ?? {}) as Record<string, unknown>;
  const key = keyOf(s, a);
  const vals = values(s, a);
  const [ask, ing, doing, done, failed] = T[key].map((tpl) => fill(tpl, vals));
  return {
    ask,
    ing,
    doing: doing.length ? doing : [...cap(ing), { k: 'text', v: '…' }],
    done,
    failed: failed.length ? failed : [{ k: 'text', v: "Couldn't " }, ...ask],
    family: key === 'mcp' ? `mcp:${vals.server}` : key,
  };
}

export const prefix = (text: string, p: Phrase): Phrase => fill(text, {}).concat(p);

/** The words of a phrase, for labels, announcements and tests. */
export function plain(p: Phrase): string {
  return p.map((s) => s.v).join('');
}

/** The line a step shows in its state (§7.4 table). */
export function lineFor(p: Phrases, state: StepState): Phrase {
  switch (state) {
    case 'running':
      return p.doing;
    case 'waiting':
      return prefix('Waiting for approval to ', p.ask);
    case 'done':
      return p.done;
    case 'failed':
      return p.failed;
    default:
      return prefix("Didn't ", p.ask);
  }
}

export interface StateInput {
  /** run.tool_result status, or the stored tool call's. */
  status?: ToolCallStatus;
  /** A persisted result exists; isError says how it ended. */
  result?: { isError?: boolean };
  verdict?: string;
  approval?: { status: ApprovalStatus };
  /** The run is still going, so a step without a result is still working. */
  live: boolean;
}

// The past tense waits for the result: a step is never "done" before its
// result arrives, whatever the approval says (§7.4 State and tense).
export function stepState(i: StateInput): StepState {
  const a = i.approval?.status;
  if (a === 'pending' || a === 'parked') return 'waiting';
  if (a === 'denied' || a === 'cancelled') return 'denied';
  if (a === 'expired') return 'expired';
  if (i.verdict === 'deny') return 'blocked';
  if (i.status === 'denied') return i.approval ? 'denied' : 'blocked';
  if (i.status === 'ok') return 'done';
  if (i.status === 'error' || i.status === 'timeout' || i.status === 'invalid') return 'failed';
  if (i.result) return i.result.isError ? 'failed' : 'done';
  if (!i.live || i.status === 'stopped') return 'denied';
  if (i.status === 'waiting' || (i.verdict === 'ask' && !i.approval && (!i.status || i.status === 'planned'))) return 'waiting';
  return 'running';
}

/** Steps that can fold under a summary; waiting, refused and approval steps always show. */
export const foldable = (state: StepState) => state === 'done' || state === 'failed';

// Short forms for a family in a summary, by how many steps it has.
function familyPhrase(family: string, steps: { input: StepInput; phrases: Phrases }[]): Phrase | null {
  const n = steps.length;
  const first = steps[0];
  const vals = (s = first) => values(s.input, (s.input.action?.payload ?? s.input.args ?? {}) as Record<string, unknown>);
  switch (family) {
    case 'fs_read': {
      if (n === 1) return first.phrases.done;
      const dirs = new Set(
        steps.map((s) => {
          const p = vals(s).path;
          return p.slice(0, Math.max(0, p.lastIndexOf('/')));
        }),
      );
      const [dir] = dirs;
      return dirs.size === 1 && dir ? fill(`Read ${n} files in {dir}`, { dir }) : fill(`Read ${n} files`, {});
    }
    case 'web_search':
      return fill(n === 1 ? 'Searched the web' : `Searched the web ${n} times`, {});
    case 'repo_checkout':
      return n === 1 ? fill('Got a copy of {repo}', vals()) : fill(`Got copies of ${n} repositories`, {});
    case 'web_fetch:get':
      return n === 1 ? fill('Read {host}', vals()) : fill(`Read ${n} web pages`, {});
  }
  if (n === 1) return first.phrases.done;
  const many: Record<string, string> = {
    fs_list: `Looked in ${n} folders`,
    'fs_write:create': `Created ${n} files`,
    'fs_write:overwrite': `Changed ${n} files`,
    'fs_write:append': `Added to ${n} files`,
    fs_delete: `Deleted ${n} files`,
    shell: `Ran ${n} commands`,
    python: `Ran ${n} Python scripts`,
    'web_fetch:send': `Sent data to ${n} websites`,
    session_search: `Searched past chats ${n} times`,
    memory: `Remembered ${n} things`,
    knowledge_search: `Searched project knowledge ${n} times`,
  };
  if (many[family]) return fill(many[family], {});
  if (family.startsWith('mcp:')) return fill(`Used {server} ${n} times`, { server: family.slice(4) });
  return null;
}

const lowerFirst = (p: Phrase): Phrase =>
  p[0]?.k === 'text' && !/^.[A-Z]/.test(p[0].v) ? [{ k: 'text', v: p[0].v.charAt(0).toLowerCase() + p[0].v.slice(1) }, ...p.slice(1)] : p;

/**
 * The line a finished group folds into (§7.4): one or two kinds of step are
 * named ("Read 3 files and searched the web"); more is "Took {n} steps"; any
 * failure is counted ("Took 4 steps, 1 didn't work").
 */
export function summarize(steps: { input: StepInput; phrases: Phrases; state: StepState }[]): { phrase: Phrase; failed: number } {
  const failed = steps.filter((s) => s.state === 'failed').length;
  const n = steps.length;
  if (failed) return { phrase: fill(`Took ${n} steps, ${failed} didn't work`, {}), failed };
  const families = new Map<string, typeof steps>();
  for (const s of steps) families.set(s.phrases.family, [...(families.get(s.phrases.family) ?? []), s]);
  if (families.size <= 2) {
    const parts = [...families].map(([f, ss]) => familyPhrase(f, ss));
    if (parts.every((p): p is Phrase => !!p)) {
      const [a, b] = parts;
      return { phrase: b ? [...a, { k: 'text', v: ' and ' }, ...lowerFirst(b)] : a, failed };
    }
  }
  return { phrase: fill(`Took ${n} steps`, {}), failed };
}

/**
 * An MCP tool's title without the server's "MCP <server>: " prefix, and a
 * bare tool name ("read_wiki_structure") in words, since the line already
 * names the server.
 */
function mcpTitle(t: string | undefined): string {
  const bare = (t ?? '').replace(/^MCP [^:]+: /, '');
  return /^[a-z0-9_-]+$/.test(bare) ? humanise(bare) : bare;
}
