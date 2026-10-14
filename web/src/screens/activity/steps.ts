// The lines under "What Bothy did" on a task (docs/design/ui.md §10.2, §7.4).
// A local copy of the §7.4 phrases for finished tool calls, so the page works
// before C3's lib/activity.ts lands; C10 swaps it for that table and
// ActivityLine. Templates mark verbatim values with backticks (shown in
// text-code) and fill {names} from the action.
import type { Action, Approval, ToolCallRecord } from '../../lib/api/types.gen';

type Templates = [ask: string, done: string, failed: string];

const table: Record<string, Templates> = {
  fs_read: ['read `{path}`', 'Read `{path}`', "Couldn't read `{path}`"],
  fs_list: ['look in `{path}`', 'Looked in `{path}`', "Couldn't open `{path}`"],
  fs_delete: ['delete `{path}`', 'Deleted `{path}`', "Couldn't delete `{path}`"],
  shell: ['run `{command}`', 'Ran `{command}`', '`{command}` finished with errors'],
  python: ['run a Python script', 'Ran a Python script', 'The script stopped with an error'],
  web_search: ['search the web', 'Searched the web for “{q}”', "Web search isn't working right now"],
  net_allow: ['reach `{host}`', 'Can now reach `{host}`', "Couldn't reach `{host}`"],
  memory: ['remember something', 'Remembered “{note}”', "Couldn't save that"],
  session_search: ['search past chats', 'Searched past chats for “{q}”', "Couldn't search past chats"],
  message: ['send a message to {to}', 'Sent a message to {to}', "Couldn't send the message"],
  skill_load: ['follow the “{skill}” skill', 'Followed the “{skill}” skill', "Couldn't follow the “{skill}” skill"],
  schedule_list: ['check the schedules', 'Checked the schedules', "Couldn't check the schedules"],
  schedule_create: ['create the schedule “{name}”', 'Created the schedule “{name}”', "Couldn't create the schedule"],
  schedule_delete: ['delete the schedule “{name}”', 'Deleted the schedule “{name}”', "Couldn't delete the schedule"],
  repo_list: ['check the connected repositories', 'Checked the connected repositories', "Couldn't check the repositories"],
  repo_checkout: ['get a copy of {repo}', 'Got a copy of {repo} (`{ref}`)', "Couldn't get {repo}"],
  repo_changes: ['review changes in {repo}', 'Reviewed changes in {repo}', "Couldn't review changes in {repo}"],
  repo_open_pr: ['open a pull request in {repo}', 'Opened pull request #{n} in {repo}', "Couldn't open the pull request"],
  github_issues: ['read issues in {repo}', 'Read issues in {repo}', "Couldn't read issues in {repo}"],
  github_pr: ['read pull request #{n}', 'Read pull request #{n} in {repo}', "Couldn't read pull request #{n}"],
  github_comment: ['comment on #{n} in {repo}', 'Commented on #{n} in {repo}', "Couldn't post the comment"],
  github_runs: ['check builds in {repo}', 'Checked recent builds in {repo}', "Couldn't check builds in {repo}"],
  release_deploy: ['deploy {repo} to {env}', 'Deployed {repo} to {env}', 'Deploy to {env} failed'],
  release_status: ['check the {env} deploy', 'Checked the {env} deploy', "Couldn't check the {env} deploy"],
  knowledge_search: ['search project knowledge', 'Searched project knowledge for “{q}”', "Couldn't search project knowledge"],
  notes: ['update the notes for {repo}', 'Updated the notes for {repo}', "Couldn't update the notes for {repo}"],
};
table.skill_read = table.skill_load;

const gerunds: Record<string, string> = { run: 'running', get: 'getting', comment: 'commenting' };
const gerund = (verb: string) => gerunds[verb] ?? (verb.endsWith('e') && verb !== 'see' ? verb.slice(0, -1) : verb) + 'ing';

const str = (v: unknown) => (typeof v === 'string' ? v : typeof v === 'number' ? String(v) : '');
const humanise = (s: string) => (s ? s[0].toUpperCase() + s.slice(1).replace(/[_-]+/g, ' ') : s);

function templates(tool: string, a: Action | undefined): Templates {
  if (tool === 'fs_write') {
    const d = a?.diff ?? '';
    const added = (d.match(/^\+(?!\+\+)/gm) ?? []).length;
    const removed = (d.match(/^-(?!--)/gm) ?? []).length;
    return d ? ['change `{path}`', `Changed \`{path}\` (+${added} −${removed})`, "Couldn't save `{path}`"] : ['create `{path}`', 'Created `{path}`', "Couldn't save `{path}`"];
  }
  if (tool === 'web_fetch') {
    return (a?.method ?? 'GET').toUpperCase() === 'GET'
      ? ['read `{host}`', 'Read `{host}`', "Couldn't open `{host}`"]
      : ['send data to `{host}`', 'Sent data to `{host}`', "Couldn't reach `{host}`"];
  }
  if (tool.startsWith('mcp__')) return ['use {server}: {title}', 'Used {server}: {title}', '{server}: {title} failed'];
  return table[tool] ?? ['use {tool}', 'Used {tool}', '{tool} failed'];
}

function vars(tool: string, a: Action | undefined): Record<string, string> {
  const p = (a?.payload ?? {}) as Record<string, unknown>;
  const argv = a?.argv ?? [];
  const command = argv[0] === 'bash' && argv[1] === '-lc' ? str(argv[2]) : argv.length ? argv.join(' ') : str(p.command);
  const [, server = '', name = ''] = tool.split('__');
  return {
    path: a?.paths?.[0] ?? (str(p.path) || '.'),
    command,
    q: str(p.query ?? p.q),
    host: a?.host ?? str(p.host),
    note: str(p.content ?? p.text).slice(0, 60),
    to: a?.recipient ?? str(p.to ?? p.chat),
    skill: str(p.name ?? p.skill),
    name: str(p.name),
    repo: str(p.repo ?? p.repository),
    ref: str(p.ref ?? p.branch) || 'main',
    n: str(p.number ?? p.pr ?? p.issue),
    env: str(p.environment ?? p.env),
    server: humanise(a?.mcp_server ?? server),
    title: humanise(a?.mcp_tool ?? name),
    tool,
  };
}

export type Segment = { text: string; code: boolean };

/** Splits a filled template into plain and verbatim (code) runs. */
export function fill(template: string, v: Record<string, string>): Segment[] {
  return template
    .split('`')
    .map((part, i) => ({ text: part.replace(/\{(\w+)\}/g, (_, k: string) => v[k] ?? ''), code: i % 2 === 1 }))
    .filter((s) => s.text);
}

export type StepState = 'running' | 'waiting' | 'done' | 'failed' | 'denied';

export function stepState(status: string): StepState {
  if (status === 'ok') return 'done';
  if (status === 'planned' || status === 'waiting') return 'waiting';
  if (status === 'approved' || status === 'running') return 'running';
  if (status === 'denied' || status === 'stopped') return 'denied';
  return 'failed';
}

export interface StepLine {
  key: string;
  state: StepState;
  segments: Segment[];
  call?: ToolCallRecord;
  approval?: Approval;
}

/** The line for one tool call; the tense follows the state, never ahead of the result. */
export function stepLine(t: ToolCallRecord): StepLine {
  const v = vars(t.tool, t.action);
  const [ask, done, failed] = templates(t.tool, t.action);
  const state = stepState(t.status);
  const text = { done, failed, waiting: `Waiting for approval to ${ask}`, denied: `Didn't ${ask}`, running: `${humanise(gerund(ask.split(' ')[0]))} ${ask.split(' ').slice(1).join(' ')}…` }[state];
  return { key: t.id, state, segments: fill(text, v), call: t };
}

/** "Maya Chen approved opening a pull request in acme/web". */
export function decisionLine(a: Approval, who: string): StepLine | null {
  if (!a.action) return null;
  const v = vars(a.action.tool, a.action);
  const [ask] = templates(a.action.tool, a.action);
  const [verb, ...rest] = ask.split(' ');
  const ing = [gerund(verb), ...rest].join(' ');
  const text = a.status === 'allowed' ? `${who} approved ${ing}` : a.status === 'denied' ? `${who} denied ${ing}` : a.status === 'expired' ? `Nobody answered, so Bothy didn't ${ask}` : '';
  return text ? { key: a.id, state: a.status === 'allowed' ? 'done' : 'denied', segments: fill(text, v), approval: a } : null;
}

/** "How it was allowed" in a step's Details. */
export function allowedHow(t: ToolCallRecord, approval: Approval | undefined, who: string): string {
  if (approval?.status === 'allowed') return who === 'You' ? 'You approved it' : `${who} approved it`;
  if (t.rule.startsWith('grant:')) return 'Allowed by a saved permission';
  if (t.verdict === 'auto') return 'Allowed automatically';
  if (t.verdict === 'deny') return 'Blocked by a rule';
  return '';
}
