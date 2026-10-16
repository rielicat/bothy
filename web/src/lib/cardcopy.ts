// Plain approval-card copy (docs/design/ui.md §8.2–8.4). The server will
// render these strings into the card itself (S2) so chat apps say the same
// words; until then they are built here from the structured Action. Only the
// known fields of each tool's payload are read. The full Action stays behind
// Details as "Exact action", so nothing the card leaves out is hidden.
import { RequesterChannel, RequesterTeammate, type Action, type Approval, type ApprovalVotes, type Card } from './api/types.gen';
import { fileLines, parseDiff } from '../components/ui/diff';
import { date, expiresIn, plural, relTime, serviceName } from './format';
import { commandOf, describeGrantParts, EXACT_GRANT, hostOf } from './grants';

/** Text with identifiers (paths, hosts, commands) marked for `text-code` rendering through Verbatim. */
export type Seg = string | { code: string };

export const segText = (segs: Seg[]): string => segs.map((s) => (typeof s === 'string' ? s : s.code)).join('');

export interface PrFile {
  path: string;
  added: number;
  removed: number;
  diff: string;
}

export interface ShowContent {
  /** diff: DiffView; new-file: DiffView of a whole new file; prose: DiffView prose; code: CodeBlock; text: plain wrapped text; files: PR files. */
  kind: 'diff' | 'new-file' | 'prose' | 'code' | 'text' | 'files';
  content: string;
  path?: string;
  files?: PrFile[];
}

export interface CopyField {
  label: string;
  /** Verbatim and shown in full. */
  value?: string;
  mono?: boolean;
  /** The line next to "Show" when the value is folded: "8 lines". */
  summary?: string;
  show?: ShowContent;
  link?: { href: string; label: string };
  /** The multi-approver "Approvals" row (§8.5), filled by ApprovalVotes. */
  votes?: boolean;
}

export type CardKind = 'action' | 'pull_request' | 'deploy' | 'review';
const kinds: CardKind[] = ['action', 'pull_request', 'deploy', 'review'];

export interface CardCopy {
  kind: CardKind;
  tone: 'default' | 'destructive';
  question: Seg[];
  consequence?: string;
  /** The allow_once button: the verb naming the result. */
  primary: string;
  /** "deleting notes/staging.md", for "You approved …". */
  ing: Seg[];
  /** "delete notes/staging.md", for "Expired: Bothy didn't …". */
  ask: Seg[];
  /** What "Allow for this chat" lets through: "changes to files in notes/". */
  grant: Seg[];
  warnings: string[];
  fields: CopyField[];
}

type Payload = Record<string, unknown>;

const code = (c: string): Seg => ({ code: c });
const str = (v: unknown): string => (typeof v === 'string' ? v : typeof v === 'number' ? String(v) : '');
const payloadOf = (a?: Action): Payload => (a?.payload && typeof a.payload === 'object' && !Array.isArray(a.payload) ? (a.payload as Payload) : {});
const lineCount = (t: string) => (t ? fileLines(t).length : 0);
const capital = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s);
// Decided lines are one line in a list; the card above them keeps the full value.
const clip = (s: string, n = 80) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

const apps: Record<string, string> = { telegram: 'Telegram', slack: 'Slack', discord: 'Discord', web: 'the web', api: 'the API' };

/** "Telegram" for "telegram". */
export function appName(channel: string | undefined): string {
  if (!channel) return '';
  return apps[channel.toLowerCase()] ?? capital(channel);
}

/** "the Telegram chat" for a chat key ("telegram:42:"), as the server's chatName says it; "this chat" without one. */
export function chatName(key: string | undefined): string {
  const i = key?.indexOf(':') ?? -1;
  return key && i > 0 ? `the ${appName(key.slice(0, i))} chat` : 'this chat';
}

/** A recipient as the server's recipientText shows it: "Telegram chat 42", "acme/web on GitHub", anything else verbatim. */
export function recipientName(key: string): string {
  const i = key.indexOf(':');
  if (i < 0) return key;
  const ch = key.slice(0, i);
  const rest = key.slice(i + 1);
  if (ch === 'github') return `${rest} on GitHub`;
  if (!['telegram', 'slack', 'discord'].includes(ch)) return key;
  const j = rest.indexOf(':');
  const thread = j < 0 ? '' : rest.slice(j + 1);
  return `${appName(ch)} chat ${j < 0 ? rest : rest.slice(0, j)}${thread ? `, thread ${thread}` : ''}`;
}

/** "create_issue" → "Create issue", "dueDate" → "Due date". */
export function humanize(key: string): string {
  const words = key
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[_\-.]+/g, ' ')
    .trim()
    .toLowerCase();
  return capital(words);
}

/** "Alex Kim", "Alex Kim and Maya Chen", "Alex Kim, Maya Chen and Priya Rao". */
export function joinNames(names: string[]): string {
  if (names.length <= 1) return names[0] ?? '';
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

/** Splits a multi-file diff (git style) into one diff per file with its counts. */
export function splitFiles(diff: string): PrFile[] {
  const start = diff.search(/^diff --git /m);
  const body = start >= 0 ? diff.slice(start) : diff;
  const chunks = start >= 0 ? body.split(/^(?=diff --git )/m).filter((c) => c.trim()) : [body];
  return chunks
    .map((d) => {
      const p = parseDiff(d);
      const git = /^diff --git a\/(\S+) b\/(\S+)/.exec(d);
      return { path: p.path ?? git?.[2] ?? '', added: p.added, removed: p.removed, diff: d };
    })
    .filter((f) => f.path || f.added || f.removed);
}

export interface Origin {
  /** "Telegram" when the request came from a chat app. */
  app?: string;
  group?: boolean;
  /** The chat's title, when the server named it. */
  title?: string;
  schedule?: boolean;
}

/**
 * Where a request came from, read from the card's context line
 * ("Telegram · DM · session “Q3 report”") and the chat reference.
 */
export function originOf(ap: Approval): Origin {
  const parts = (ap.card?.context ?? '')
    .split(' · ')
    .map((p) => p.trim())
    .filter(Boolean);
  let title: string | undefined;
  const quoted = /^(?:session|schedule) “(.*)”$/i.exec(parts[parts.length - 1] ?? '');
  if (quoted) {
    title = quoted[1];
    parts.pop();
  }
  // A chat without a title is named by its id on the server; ids never reach the main flow.
  if (title && /^[a-z]{3}_[0-9a-z]{6,}$/i.test(title)) title = undefined;
  // The first part names where the chat came from; anything else there is not an app.
  const channel = (ap.chat?.channel ?? (/^[a-z]+$/i.test(parts[0] ?? '') ? parts[0] : '')).toLowerCase();
  return {
    app: channel && channel !== 'web' && channel !== 'api' ? appName(channel) : undefined,
    group: ap.chat?.kind === 'group' || parts.some((p) => p.toLowerCase() === 'group'),
    title,
    schedule: parts.some((p) => /^(schedule|heartbeat)$/i.test(p)),
  };
}

/** A request from a person or a channel in a chat app, which an admin or a chat approver decides (§8.4). */
export function fromTeam(ap: Approval): boolean {
  return !!ap.approver?.team || !!ap.principal_id?.startsWith('spc_');
}

/**
 * "Maya Chen (@maya) asked on Slack.", "Alex asked in #eng on Slack."; a
 * schedule's, which nobody asked for: "From Maya Chen's schedule on Slack.",
 * "From #eng on Slack."; "" when the server named nobody.
 */
export function teamAsked(ap: Approval): string {
  const r = ap.requester;
  if (!r?.name) return '';
  const app = appName(r.app || ap.chat?.channel);
  const channel = r.kind === RequesterChannel;
  if (r.scheduled) return channel ? `From ${r.chat || r.name} on ${app}.` : `From ${r.name}'s schedule on ${app}.`;
  if (channel) return `${r.name} asked in ${r.chat} on ${app}.`;
  return `${r.name}${r.handle ? ` (@${r.handle})` : ''} asked on ${app}.`;
}

/** The person whose own chat a team request came from, cut to max for button labels; '' for a channel's. */
export function teamPerson(ap: Approval, max = 24): string {
  const r = ap.requester;
  if (!fromTeam(ap) || !r?.name || r.kind !== RequesterTeammate) return '';
  return clip(r.name, max);
}

/** Who reads a deny reason on a team request: the person in their chat; a channel's outcome line leaves it out. */
export function denyAudience(ap: Approval): string {
  const r = ap.requester;
  if (!fromTeam(ap) || !r?.name) return '';
  return r.kind === RequesterChannel ? `People in ${r.chat || r.name} won't see this, but Bothy will.` : `${r.name} will see this.`;
}

/** The server's warnings in the words of §8.3. */
export function plainWarnings(ap: Approval): string[] {
  const a = ap.action;
  const out: string[] = [];
  for (const w of ap.card?.warnings ?? []) {
    let t = w;
    const first = /^First contact with (.+?)\.?$/.exec(w);
    if (first) {
      const target = first[1].replace(/^github:/, '');
      if (a?.tool === 'repo_open_pr') t = `This is the first pull request Bothy opens in ${target} for ${ap.principal_id.startsWith('spc_') ? 'this group chat' : 'you'}.`;
      else if (a?.kind === 'http' || a?.kind === 'net' || /^[a-z0-9.-]+\.[a-z]{2,}(:\d+)?$/i.test(target)) t = `Bothy hasn't sent data to ${target} before.`;
      else t = `Bothy hasn't sent anything to ${recipientName(target)} before.`;
    } else if (/first time sending to this recipient/i.test(w)) {
      t = `Bothy hasn't sent anything to ${a?.recipient ? recipientName(a.recipient) : 'this recipient'} before.`;
    } else if (/private (data|information).*untrusted|untrusted.*private|read untrusted|holds private/i.test(w)) {
      t = 'This chat has seen private information and content from outside. Check carefully before letting anything leave.';
    } else if (/untrusted sender|untrusted (content|origin)/i.test(w)) {
      t = 'This request came from content Bothy read (a web page, repository or message), not from a person.';
    } else if (/internationali[sz]ed domain name/i.test(w)) {
      t = 'This address uses international characters, so it is shown in its plain form.';
    }
    if (!out.includes(t)) out.push(t);
  }
  return out;
}

interface Built {
  question: Seg[];
  primary: string;
  ing: Seg[];
  ask: Seg[];
  consequence?: string;
  fields: (CopyField | null | undefined | false | "")[];
  /** The grant's words when the gateway pins the whole request (memory, notes). */
  exact?: Seg[];
  kind?: CardKind;
  destructive?: boolean;
}

const UNDONE = "This can't be undone.";

/** Up to eight top-level payload values as fields, labels humanised; the rest is in the exact Action. */
function payloadFields(p: Payload, skip: string[] = []): CopyField[] {
  const out: CopyField[] = [];
  for (const [k, v] of Object.entries(p)) {
    if (skip.includes(k) || v === null || v === undefined || v === '') continue;
    if (typeof v === 'string') out.push({ label: humanize(k), value: v });
    else if (typeof v === 'number' || typeof v === 'boolean') out.push({ label: humanize(k), value: String(v) });
    else out.push({ label: humanize(k), value: JSON.stringify(v, null, 2), mono: true });
    if (out.length === 8) break;
  }
  return out;
}

function fsWrite(a: Action, p: Payload, card: Card): Built {
  const path = str(p.path) || a.paths?.[0] || '';
  const diff = a.diff || card.diff || '';
  const content = str(p.content);
  const file: CopyField = { label: 'File', value: path, mono: true };
  if (str(p.mode) === 'append') {
    const body = content || diff;
    return {
      question: ['Add to ', code(path), '?'],
      primary: 'Add to file',
      ing: ['adding to ', code(path)],
      ask: ['add to ', code(path)],
      fields: [file, { label: 'Adds', summary: plural(lineCount(body), 'line'), show: { kind: 'new-file', content: body, path } }],
    };
  }
  if (str(p.mode) === 'create' || /^--- \/dev\/null/m.test(diff)) {
    const body = content || diff;
    return {
      question: ['Create ', code(path), '?'],
      primary: 'Create file',
      ing: ['creating ', code(path)],
      ask: ['create ', code(path)],
      fields: [file, { label: 'Content', summary: plural(lineCount(body), 'line'), show: { kind: 'new-file', content: body, path } }],
    };
  }
  const d = parseDiff(diff);
  return {
    question: ['Change ', code(path), '?'],
    primary: 'Save changes',
    ing: ['changing ', code(path)],
    ask: ['change ', code(path)],
    fields: [
      file,
      d.rows.length
        ? { label: 'Changes', summary: `+${d.added} −${d.removed}`, show: { kind: 'diff', content: diff, path } }
        : content && { label: 'Content', summary: plural(lineCount(content), 'line'), show: { kind: 'code', content } },
    ],
  };
}

function fsDelete(a: Action, p: Payload, card: Card): Built {
  const path = str(p.path) || a.paths?.[0] || '';
  const diff = a.diff || card.diff || '';
  const folder = /^\(folder with (\d+) top-level entries\)$/m.exec(diff);
  const base = { question: ['Delete ', code(path), '?'], ing: ['deleting ', code(path)], ask: ['delete ', code(path)], consequence: UNDONE, destructive: true };
  if (folder) {
    return { ...base, primary: 'Delete folder', fields: [{ label: 'Folder', value: path, mono: true }, { label: 'Contains', value: plural(Number(folder[1]), 'item') }] };
  }
  const gone = diff && parseDiff(diff).removed ? fileLines(diff) : [];
  return {
    ...base,
    primary: 'Delete file',
    fields: [{ label: 'File', value: path, mono: true }, gone.length > 0 && { label: 'Content', summary: plural(gone.length, 'line'), show: { kind: 'code', content: gone.join('\n') } }],
  };
}

function shell(a: Action, p: Payload, card: Card): Built {
  const cmd = commandOf(a) || card.fields?.find((f) => f.label === 'Command')?.value || card.code || '';
  const cwd = str(p.cwd) || a.cwd || '';
  return {
    question: ['Run this command?'],
    primary: 'Run command',
    ing: ['running ', code(clip(cmd))],
    ask: ['run ', code(clip(cmd))],
    fields: [{ label: 'Command', value: cmd, mono: true }, cwd && cwd !== '.' && cwd !== '/workspace' && { label: 'In', value: cwd, mono: true }],
  };
}

function python(_a: Action, p: Payload, card: Card): Built {
  const script = str(p.code) || card.code || '';
  return {
    question: ['Run this Python script?'],
    primary: 'Run script',
    ing: ['running a Python script'],
    ask: ['run a Python script'],
    fields: [{ label: 'Script', summary: plural(lineCount(script), 'line'), show: { kind: 'code', content: script } }],
  };
}

function webFetch(a: Action, p: Payload): Built {
  const host = hostOf(a) || hostOf({ url: str(p.url) });
  const url = a.url || str(p.url);
  if (!a.method || a.method.toUpperCase() === 'GET') {
    return { question: ['Read ', code(host), '?'], primary: 'Read page', ing: ['reading ', code(host)], ask: ['read ', code(host)], fields: [{ label: 'Website', value: url, mono: true }] };
  }
  const data = payloadFields(p, ['url', 'method']);
  return {
    question: ['Send data to ', code(host), '?'],
    primary: 'Send data',
    ing: ['sending data to ', code(host)],
    ask: ['send data to ', code(host)],
    fields: [
      { label: 'Website', value: url, mono: true },
      { label: 'What', value: 'Sends data' },
      data.length > 0 && { label: 'Data', show: { kind: 'code', content: JSON.stringify(p, null, 2) } },
    ],
  };
}

function netAllow(a: Action, p: Payload): Built {
  const host = hostOf(a) || str(p.host);
  return {
    question: ['Let Bothy reach ', code(host), '?'],
    primary: 'Allow for this chat',
    ing: ['letting Bothy reach ', code(host)],
    ask: ['reach ', code(host)],
    fields: [{ label: 'Website', value: host, mono: true }],
  };
}

function message(a: Action, p: Payload, card: Card): Built {
  const to = a.recipient ?? '';
  const chat = chatName(to);
  const toText = card?.fields?.find((f) => f.label === 'To')?.value || (to && recipientName(to));
  return {
    question: [`Send a message to ${chat}?`],
    primary: 'Send message',
    ing: [`sending a message to ${chat}`],
    ask: [`send a message to ${chat}`],
    fields: [toText && { label: 'To', value: toText }, { label: 'Message', value: str(p.text) }],
  };
}

function memory(_a: Action, p: Payload): Built {
  if (str(p.op) === 'remove') {
    const old = str(p.old);
    return { question: ['Forget this?'], primary: 'Forget', ing: [`forgetting “${clip(old, 60)}”`], ask: ['forget something'], exact: ['forgetting this exact note'], fields: [{ label: 'Note', value: old }] };
  }
  const note = str(p.new);
  return {
    question: ['Remember this?'],
    primary: 'Remember',
    ing: [`remembering “${clip(note, 60)}”`],
    ask: ['remember something'],
    exact: ['remembering this exact note'],
    fields: [{ label: 'Note', value: note }, str(p.op) === 'replace' && { label: 'Replaces', value: str(p.old) }],
  };
}

function notes(a: Action, p: Payload, card: Card): Built {
  const repo = str(p.repo);
  const diff = a.diff || card.diff || '';
  return {
    question: ['Update the notes for ', code(repo), '?'],
    primary: 'Update notes',
    ing: ['updating the notes for ', code(repo)],
    ask: ['update the notes for ', code(repo)],
    exact: ['making this exact change to the notes for ', code(repo)],
    fields: [diff && { label: 'Change', show: { kind: 'prose', content: diff } }],
  };
}

// Where a schedule's results go, as internal/policy/copy.go deliverText says it. Without a set chat an account's
// stay on the web, a teammate's go to their direct messages and a channel's to the channel.
function sendsTo(p: Payload): string {
  const deliver = p.deliver && typeof p.deliver === 'object' ? (p.deliver as { channel?: string }) : undefined;
  if (deliver) return deliver.channel && deliver.channel !== 'web' ? appName(deliver.channel) : 'Bothy on the web';
  const owner = str(p.owner);
  const usual = str(p.usual_chat);
  const kind = str(p.owner_kind);
  if (!owner || !usual) return 'Bothy on the web';
  if (kind === 'person') return `${owner}'s messages on ${appName(usual)}`;
  if (kind === 'space') return `${owner} on ${appName(usual)}`;
  return 'Bothy on the web';
}

function scheduleCreate(_a: Action, p: Payload): Built {
  const name = str(p.name);
  const prompt = str(p.prompt);
  const lines = prompt.split('\n');
  return {
    question: [`Create the schedule “${name}”?`],
    primary: 'Create schedule',
    ing: [`creating the schedule “${name}”`],
    ask: [`create the schedule “${name}”`],
    fields: [
      { label: 'When', value: str(p.when) || str(p.spec) },
      { label: 'Sends to', value: sendsTo(p) },
      lines.length > 3 ? { label: 'Does', summary: lines.slice(0, 3).join('\n'), show: { kind: 'text', content: prompt } } : { label: 'Does', value: prompt },
    ],
  };
}

function scheduleDelete(_a: Action, p: Payload): Built {
  const name = str(p.name);
  return {
    question: [`Delete the schedule “${name}”?`],
    primary: 'Delete schedule',
    ing: [`deleting the schedule “${name}”`],
    ask: [`delete the schedule “${name}”`],
    consequence: UNDONE,
    // Deleting a schedule can't be undone, whatever its side-effect class (§8.2).
    destructive: true,
    fields: [str(p.when) && { label: 'When', value: str(p.when) }],
  };
}

function openPR(a: Action, p: Payload, card: Card): Built {
  const repo = str(p.repo) || (a.recipient ?? '').replace(/^github:/, '');
  const files = splitFiles(a.diff || card.diff || '');
  const added = files.reduce((n, f) => n + f.added, 0);
  const removed = files.reduce((n, f) => n + f.removed, 0);
  return {
    kind: 'pull_request',
    question: ['Open a pull request in ', code(repo), '?'],
    primary: 'Open pull request',
    ing: ['opening a pull request in ', code(repo)],
    ask: ['open a pull request in ', code(repo)],
    fields: [
      { label: 'Branch', value: `${str(p.head)} → ${str(p.base)}`, mono: true },
      { label: 'Title', value: str(p.title) },
      str(p.body).trim() && { label: 'Description', value: str(p.body) },
      files.length > 0 && { label: 'Changes', summary: `${plural(files.length, 'file')}, +${added} −${removed}`, show: { kind: 'files', content: a.diff || card.diff || '', files } },
    ],
  };
}

function githubComment(_a: Action, p: Payload): Built {
  const repo = str(p.repo);
  const n = str(p.number);
  return {
    question: [`Comment on #${n} in `, code(repo), '?'],
    primary: 'Post comment',
    ing: [`commenting on #${n} in `, code(repo)],
    ask: [`comment on #${n} in `, code(repo)],
    fields: [{ label: 'On', value: `#${n} in ${repo}` }, { label: 'Comment', value: str(p.body) }],
  };
}

function deploy(_a: Action, p: Payload): Built {
  const env = str(p.environment);
  const repo = str(p.repo);
  const inputs = p.inputs && typeof p.inputs === 'object' ? Object.entries(p.inputs as Record<string, unknown>) : [];
  return {
    kind: 'deploy',
    question: ['Deploy ', code(repo), ` to ${env}?`],
    primary: 'Approve deploy',
    ing: ['deploying ', code(repo), ` to ${env}`],
    ask: ['deploy ', code(repo), ` to ${env}`],
    consequence: `This updates ${env} for everyone using it.`,
    fields: [
      { label: 'Environment', value: env },
      { label: 'Version', value: str(p.ref), mono: true },
      str(p.workflow) && { label: 'Workflow', value: str(p.workflow), mono: true },
      inputs.length > 0 && { label: 'Inputs', value: inputs.map(([k, v]) => `${k}=${str(v)}`).join('\n'), mono: true },
      { label: 'Approvals', votes: true },
    ],
  };
}

function mcp(a: Action, p: Payload): Built {
  const [, srv = '', tl = ''] = a.tool.split('__');
  const server = serviceName(a.mcp_server || srv);
  const title = humanize(a.mcp_tool || tl || a.tool);
  const what = `${server}: ${title}`;
  const skip = Object.entries(p)
    .filter(([, v]) => v === a.recipient || v === a.url)
    .map(([k]) => k);
  return {
    question: [`Use ${what}?`],
    primary: 'Allow',
    ing: [`using ${what}`],
    ask: [`use ${what}`],
    fields: [a.recipient && { label: 'To', value: a.recipient }, a.url && { label: 'Website', value: a.url, mono: true }, ...payloadFields(p, skip)],
  };
}

function financial(a: Action, p: Payload): Built {
  const amount = str(p.amount);
  const to = a.recipient || str(p.to) || str(p.recipient);
  const what = amount && to ? `Pay ${amount} to ${to}?` : 'Make this payment?';
  return {
    question: [what],
    primary: amount ? `Pay ${amount}` : 'Pay',
    ing: [amount ? `paying ${amount}` : 'paying'],
    ask: [amount ? `pay ${amount}` : 'pay'],
    consequence: "Money will leave your account. This can't be undone.",
    destructive: true,
    fields: [to && { label: 'To', value: to }, amount && { label: 'Amount', value: amount }],
  };
}

function unknown(a: Action, p: Payload): Built {
  const name = humanize(a.tool);
  if (a.side_effect === 'admin') {
    return { question: ["Change Bothy's settings?"], primary: 'Allow', ing: ["changing Bothy's settings"], ask: ["change Bothy's settings"], fields: payloadFields(p) };
  }
  return { question: [`Let Bothy use ${name}?`], primary: 'Allow', ing: [`using ${name}`], ask: [`use ${name}`], fields: payloadFields(p) };
}

const builders: Record<string, (a: Action, p: Payload, card: Card) => Built> = {
  fs_write: fsWrite,
  fs_delete: fsDelete,
  shell,
  python,
  web_fetch: webFetch,
  net_allow: netAllow,
  message,
  memory,
  notes,
  schedule_create: scheduleCreate,
  schedule_delete: scheduleDelete,
  repo_open_pr: openPR,
  github_comment: githubComment,
  release_deploy: deploy,
};

// v0.1 card labels, for approvals that arrive without an Action.
const legacyLabels: Record<string, string> = { Path: 'File', Paths: 'Files', Recipient: 'To', URL: 'Website', Network: 'Website', Folder: 'In', 'MCP server': 'Server', 'MCP tool': 'Action' };

function legacy(card: Card): Built {
  const title = card.title || 'Allow this';
  const fields: CopyField[] = (card.fields ?? [])
    .filter((f) => !['Tool', 'Host', 'Approvals needed'].includes(f.label))
    .map((f) => ({ label: legacyLabels[f.label] ?? f.label, value: f.value, mono: f.mono }));
  if (card.code) fields.push({ label: 'Content', summary: plural(lineCount(card.code), 'line'), show: { kind: 'code', content: card.code } });
  if (card.diff) {
    const d = parseDiff(card.diff);
    fields.push({ label: 'Changes', summary: `+${d.added} −${d.removed}`, show: { kind: 'diff', content: card.diff } });
  }
  const lower = title[0].toLowerCase() + title.slice(1);
  return { question: [`${title}?`], primary: 'Allow', ing: [lower.replace(/^(\w+)/, (w) => (w.endsWith('e') ? w.slice(0, -1) : w) + 'ing')], ask: [lower], fields };
}

// A grant that pins the whole request still says what it is about: "using Linear: Create issue with these exact details".
function grantOf(a: Action | undefined, b: Built): Seg[] {
  const parts = describeGrantParts(a);
  if (!a || parts.length !== 1 || parts[0] !== EXACT_GRANT) return parts;
  return b.exact ?? [...b.ing, ' with these exact details'];
}

/** The plain copy of an approval card: from the server's S2 fields when present, else from the Action. */
export function cardCopy(ap: Approval): CardCopy {
  const card = ap.card;
  const a = ap.action;
  const p = payloadOf(a);
  let b: Built;
  if (!a) b = legacy(card);
  else if (builders[a.tool]) b = builders[a.tool](a, p, card);
  // Money first: a payment says how much and to whom, whichever connected tool makes it.
  else if (a.side_effect === 'financial') b = financial(a, p);
  else if (a.kind === 'mcp' || a.tool.startsWith('mcp__')) b = mcp(a, p);
  else b = unknown(a, p);

  const se = a?.side_effect ?? card.risk;
  const irreversible = se === 'irreversible' || se === 'financial';
  // Red marks the click that can't be taken back; a deploy's safeguard is its approvers (§8.2, D10).
  const destructive = a?.tool !== 'release_deploy' && (b.destructive || irreversible);
  const consequence = b.consequence ?? (irreversible ? UNDONE : undefined);

  const copy: CardCopy = {
    kind: b.kind ?? 'action',
    tone: destructive ? 'destructive' : 'default',
    question: b.question,
    consequence,
    primary: b.primary,
    ing: b.ing,
    ask: b.ask,
    grant: grantOf(a, b),
    warnings: plainWarnings(ap),
    fields: b.fields.filter((f): f is CopyField => !!f),
  };
  if (card.question) {
    // S2: the server's words win; the client keeps only what it adds (Show, votes).
    copy.question = [card.question];
    copy.consequence = card.consequence || undefined;
    copy.primary = card.primary_label || copy.primary;
    if (card.ing) copy.ing = [card.ing];
    // The generated kind and tone are open strings; an unknown value keeps the client's.
    if (card.kind && kinds.includes(card.kind as CardKind)) copy.kind = card.kind as CardKind;
    if (card.tone === 'default' || card.tone === 'destructive') copy.tone = card.tone;
    copy.warnings = card.warnings ?? [];
  }
  return copy;
}

/** The label a button shows while its decision is being sent: "Deleting…". */
export function busyLabel(label: string): string {
  const [verb] = label.split(' ');
  const irregular: Record<string, string> = {
    Run: 'Running',
    Deny: 'Denying',
    Pay: 'Paying',
    Cancel: 'Cancelling',
    Allow: 'Allowing',
    Always: 'Allowing',
    Forget: 'Forgetting',
    Let: 'Letting',
    Get: 'Getting',
    Set: 'Setting',
    Stop: 'Stopping',
  };
  const ing = irregular[verb] ?? (verb.endsWith('e') ? verb.slice(0, -1) + 'ing' : verb + 'ing');
  return `${ing}…`;
}

/** The expiry line: "Expires in 15 min"; urgent under two minutes; parked approvals wait without a deadline. */
export function expiryOf(ap: Approval, now = Date.now()): { text: string; urgent: boolean } | null {
  if (ap.status === 'parked') return { text: 'Bothy stopped waiting. Approve to let it carry on.', urgent: false };
  if (ap.status !== 'pending') return null;
  const at = date(ap.card?.expires) ?? date(ap.expires_at);
  if (!at) return null;
  const text = expiresIn(at.toISOString(), { now });
  if (!text || text === 'Expired') return null;
  return { text, urgent: at.getTime() - now < 2 * 60_000 };
}

/** "10:42" or "10:42 AM", as the browser's locale writes a time of day. */
export function timeOfDay(t: string | undefined): string {
  const d = date(t);
  return d ? d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }) : '';
}

export function isOpen(ap: Approval): boolean {
  return ap.status === 'pending' || ap.status === 'parked';
}

/** Multi-approver approvals are decided by votes (S4); without the S4 fields every approval is single-approver. */
export function isMulti(ap: Approval): boolean {
  const r = ap.approver;
  return !!r && !r.user_id && !r.admin_only && !r.team && (!!r.users?.length || !!r.roles?.length);
}

const rank: Record<string, number> = { member: 1, admin: 2, owner: 3 };

export interface Who {
  id: string;
  role: string;
}

/** Whose request this is: the voter whose approval never counts, or the person whose chat asked. */
export function requesterOf(ap: Approval, vv?: ApprovalVotes | null): string | undefined {
  if (isMulti(ap)) return vv?.requester || ap.approver.requester || undefined;
  return ap.principal_id?.startsWith('usr_') ? ap.principal_id : undefined;
}

/**
 * Whether a denied request was withdrawn by the person who asked: on a
 * multi-approver request their deny is the only vote they have (§8.5), so it
 * reads "Cancelled by …", never "denied".
 */
export function withdrawn(ap: Approval): boolean {
  const r = ap.approver?.requester;
  if (ap.status !== 'denied' || !r) return false;
  const denier = ap.votes?.find((v) => v.choice === 'deny')?.user_id ?? ap.approvals?.votes?.find((v) => v.choice === 'deny')?.user ?? ap.decided_by;
  return denier === r;
}

/** Whether `me` can decide this open approval now (and so sees its buttons). */
export function decidable(ap: Approval, me: Who | undefined, vv?: ApprovalVotes | null): boolean {
  if (!isOpen(ap) || !me) return false;
  const r = ap.approver;
  if (isMulti(ap)) {
    if (vv) return vv.can_vote;
    if (r.requester === me.id || ap.votes?.some((v) => v.user_id === me.id)) return false;
    return !!r.users?.includes(me.id) || !!r.roles?.some((ro) => (rank[me.role] ?? 0) >= (rank[ro] ?? 9));
  }
  // Fails closed: an approval that names no approver shows no buttons.
  return r?.admin_only || r?.team ? (rank[me.role] ?? 0) >= rank.admin : !!r?.user_id && r.user_id === me.id;
}

export interface DecidedCopy {
  glyph: 'check' | 'close';
  tone: 'accent' | 'danger' | 'muted';
  segs: Seg[];
  /** A deny reason, shown in curly quotes. */
  reason?: string;
  time?: string;
  /** Full date for the time's title. */
  at?: string;
}

export interface DecidedOpts {
  me?: string;
  /** Display name for a user id; '' when unknown. */
  name: (id: string) => string;
  votes?: ApprovalVotes | null;
  /** In a list (the Done tab) every line names what was decided. */
  list?: boolean;
  now?: number;
}

/** The one-line record of a decided approval (§8.4). */
export function decidedLine(ap: Approval, o: DecidedOpts): DecidedCopy {
  const a2 = ap;
  const copy = cardCopy(ap);
  const at = ap.decided_at && date(ap.decided_at) ? ap.decided_at : undefined;
  const now = o.now ?? Date.now();
  const day = at && new Date(at).toDateString();
  const recentDay = day === new Date(now).toDateString() || day === new Date(now - 86_400_000).toDateString();
  // The Done tab groups by Today, Yesterday and Earlier, so only the Earlier lines carry their date.
  const time = at ? (!o.list || recentDay ? timeOfDay(at) : relTime(at, { now: o.now })) : undefined;
  const who = (id?: string, name?: string) => (id && id === o.me ? 'You' : name || (id ? o.name(id) : '') || 'Someone');
  // A chat approver's name already says which app: "Sam Patel · Slack".
  const via = ap.decided_via && !ap.decided_by_identity && !['web', 'api', 'timeout', 'stop', ''].includes(ap.decided_via) ? appName(ap.decided_via) : '';
  const requester = o.votes?.requester_name || (a2.approver?.requester ? who(a2.approver.requester) : '');
  const voteName = (v: { user_id: string; name?: string }) => who(v.user_id, v.name);
  const votes = o.votes?.votes ?? a2.votes?.map((v) => ({ ...v, name: '' })) ?? [];

  if (ap.status === 'expired') return { glyph: 'close', tone: 'muted', segs: ["Expired: Bothy didn't ", ...copy.ask] };
  if (ap.status === 'cancelled') {
    const by = ap.decided_via === 'stop' ? 'Cancelled when the chat was stopped' : `Cancelled by ${requester === 'You' ? 'you' : requester || 'the person who asked'}`;
    return { glyph: 'close', tone: 'muted', segs: [by], time, at };
  }
  if (ap.status === 'denied') {
    if (ap.decided_by_identity && ap.decided_by_identity === ap.approver?.requester_identity) {
      return { glyph: 'close', tone: 'muted', segs: [`Cancelled by ${ap.requester?.name || 'the person who asked'}`], time, at };
    }
    const denial = votes.find((v) => v.choice === 'deny');
    if (denial && a2.approver?.requester && denial.user_id === a2.approver.requester) {
      return { glyph: 'close', tone: 'muted', segs: [`Cancelled by ${voteName(denial) === 'You' ? 'you' : voteName(denial)}`], time, at };
    }
    const name = denial ? voteName(denial) : who(ap.decided_by, a2.decided_by_name);
    const reason = denial?.comment || a2.comment || undefined;
    const segs: Seg[] = name !== 'You' && !o.list && !denial ? [`${name} denied${via ? ` in ${via}` : ''}`] : [`${name} denied `, ...copy.ing];
    return { glyph: 'close', tone: 'danger', segs, reason, time, at };
  }
  // allowed
  const yes = votes.filter((v) => v.choice === 'allow_once');
  if (isMulti(ap) && yes.length) return { glyph: 'check', tone: 'accent', segs: [`${joinNames(yes.map(voteName))} approved `, ...copy.ing], time, at };
  const name = who(ap.decided_by, a2.decided_by_name);
  // A teammate's standing choice is theirs, not the approver's, as its button said.
  const person = teamPerson(ap, 40);
  let segs: Seg[];
  if (ap.choice === 'allow_session') segs = [`${name} allowed `, ...copy.grant, person ? ` in ${person}'s chat` : ' for this chat'];
  else if (ap.choice === 'allow_always') segs = [`${name} ${name === 'You' ? 'always allow' : 'always allows'} `, ...copy.grant, ...(person ? [` for ${person}`] : [])];
  else if (name !== 'You' && !o.list) segs = [`${name} approved${via ? ` in ${via}` : ''}`];
  else segs = [`${name} approved `, ...copy.ing];
  return { glyph: 'check', tone: 'accent', segs, time, at };
}
