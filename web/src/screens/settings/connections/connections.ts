// Status words, failure sentences and small helpers shared by the
// Connections pages (docs/design/ui.md §12). Copy follows §12 and §5.6.4.
import { api, ApiError, errorText } from '../../../lib/api/client';
import type { ChannelStatus, MCPStatus, SideEffect } from '../../../lib/api/types.gen';
import { config } from '../../../lib/config.svelte';
import { plural, relTime, serviceName } from '../../../lib/format';
import { failure, type FailureCode } from '../../../lib/providerErrors';
import type { GitHubConnection, GitHubProbe, ReleaseEnvironment } from './types';

export type AppName = 'slack' | 'telegram' | 'discord';
export const appNames: AppName[] = ['slack', 'telegram', 'discord'];
export const appTitle: Record<AppName, string> = { slack: 'Slack', telegram: 'Telegram', discord: 'Discord' };
export const isApp = (s: string): s is AppName => (appNames as string[]).includes(s);

/** ChannelStatus plus the Slack workspace name the server doesn't send yet. */
export type AppStatus = ChannelStatus & { workspace?: string };

export interface RowStatus {
  text: string;
  tone: 'muted' | 'warning' | 'danger';
  /** The row's one button. */
  action: 'Connect' | 'Manage' | 'Reconnect' | 'Review' | 'Change';
}

export const toneClass = { muted: 'text-muted', warning: 'text-warning-text', danger: 'text-danger-text' };

export function connectedText(s: AppStatus | undefined, withIdentity = false): string {
  if (s?.workspace) return `Connected to ${s.workspace}${withIdentity && s.identity ? ` as ${s.identity}` : ''}`;
  return s?.identity ? `Connected as ${s.identity}` : 'Connected';
}

export function appStatus(app: AppName, s: AppStatus | undefined, linked: number, configured: boolean): RowStatus {
  if (!s || (!s.enabled && !configured)) return { text: 'Not connected', tone: 'muted', action: 'Connect' };
  if (!s.enabled) return { text: 'Off', tone: 'muted', action: 'Manage' };
  switch (s.state) {
    case 'connected':
      return { text: connectedText(s) + (linked ? ` · ${plural(linked, 'person', 'people')} linked` : ''), tone: 'muted', action: 'Manage' };
    case 'auth_failed':
      return { text: `Can't connect: ${appTitle[app]} didn't accept the token`, tone: 'danger', action: 'Reconnect' };
    case 'backoff':
      return { text: `Can't connect: ${appTitle[app]} isn't answering`, tone: 'danger', action: 'Reconnect' };
    default:
      return { text: 'Connecting…', tone: 'muted', action: 'Manage' };
  }
}

/** Why an MCP server can't run, as the end of "Can't connect: …" or "Can't start: …". */
export function toolProblem(s: MCPStatus): string {
  if (s.state === 'no_sandbox') return "code can't run on this server";
  return s.transport === 'stdio' ? 'the command stopped with an error' : "the server didn't answer";
}

export function toolStatus(s: MCPStatus | undefined): RowStatus {
  if (!s) return { text: 'Connecting…', tone: 'muted', action: 'Manage' };
  switch (s.state) {
    case 'connecting':
      return { text: 'Connecting…', tone: 'muted', action: 'Manage' };
    case 'disabled':
      return { text: 'Off', tone: 'muted', action: 'Manage' };
    case 'installing':
      return { text: 'Installing…', tone: 'muted', action: 'Manage' };
    case 'ready':
      return { text: `Ready · ${plural(s.tools, 'action')}`, tone: 'muted', action: 'Manage' };
    case 'pins_pending':
      return { text: `Needs review: ${plural(s.pending, 'action')}`, tone: 'warning', action: 'Review' };
    default:
      return { text: `Can't connect: ${toolProblem(s)}`, tone: 'danger', action: s.state === 'no_sandbox' ? 'Manage' : 'Reconnect' };
  }
}

export function githubStatus(c: GitHubConnection | undefined, repos: number): RowStatus {
  if (!c) return { text: 'Not connected', tone: 'muted', action: 'Connect' };
  if (c.status === 'error') return { text: `Can't connect: GitHub didn't accept the ${c.auth === 'app' ? 'app' : 'token'}`, tone: 'danger', action: 'Reconnect' };
  return { text: `Connected as ${c.account} · ${plural(repos, 'repository', 'repositories')}`, tone: 'muted', action: 'Manage' };
}

/**
 * Whether Settings → Connections carries the attention dot (§4.10): a chat app
 * or GitHub that can't connect, or a tool waiting for review. For C1's dot store.
 */
export function needsAttention(apps: AppStatus[], tools: MCPStatus[], github: GitHubConnection[]): boolean {
  return (
    apps.some((a) => a.enabled && (a.state === 'auth_failed' || a.state === 'backoff')) ||
    tools.some((t) => t.state === 'pins_pending' || t.state === 'error') ||
    github.some((g) => g.status === 'error')
  );
}

export const searchTitle: Record<string, string> = { none: 'Off', searxng: 'SearXNG', brave: 'Brave Search', tavily: 'Tavily' };

/** "linear" → "Linear", "team-docs" → "Team docs", "deepwiki" → "DeepWiki". */
export const serverTitle = serviceName;

export const effects: { value: SideEffect; label: string }[] = [
  { value: 'read', label: 'Only reads information' },
  { value: 'local_write', label: 'Changes things inside Bothy' },
  { value: 'external_send', label: 'Sends data out' },
  { value: 'irreversible', label: "Can't be undone" },
];

const roleWords: Record<string, string> = { admin: 'any admin', owner: 'the owner', member: 'any member' };

/** "2 approvals from Alex Kim, Priya Rao, any admin". */
export function approverLine(env: Pick<ReleaseEnvironment, 'required' | 'approvers'>): string {
  // The server sends no name for a user that no longer exists.
  const people = env.approvers.filter((a) => a.kind !== 'role').map((a) => a.name || 'a removed person');
  const roles = env.approvers.filter((a) => a.kind === 'role').map((a) => roleWords[a.id] ?? a.name);
  return `${plural(Math.max(env.required, 1), 'approval')} from ${[...people, ...roles].join(', ')}`;
}

/** "Read today at 09:12", "Read yesterday at 14:02", "Read 14 Oct". */
export function readAt(t: string, now = Date.now()): string {
  const d = new Date(t);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((day(new Date(now)) - day(d)) / 86_400_000);
  // Same clock as relTime ("9:12 AM" or "09:12" by locale), not the zero-padded 12 h form.
  const at = d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  if (days === 0) return `Read today at ${at}`;
  if (days === 1) return `Read yesterday at ${at}`;
  return `Read ${relTime(t, { now })}`;
}

export interface Failure {
  message: string;
  details?: string;
}

const codes = [
  'key_rejected',
  'no_model_access',
  'out_of_credit',
  'rate_limited',
  'provider_error',
  'timeout',
  'unreachable',
  'private_address',
  'no_models',
  'tls',
  'not_a_model_server',
];

/**
 * The failure category: the server's S13 code once it sends one; until then
 * HTTP status and a few known phrases (§17 S13 fallback).
 */
export function failureCode(e: unknown): string {
  const m = errorText(e).toLowerCase();
  // S13 has no code for Discord's missing intent (close 4014), so it is found by its text even then.
  if (/4014|intent/.test(m)) return 'intent';
  if (e instanceof ApiError && codes.includes(e.code)) return e.code;
  if (/private (network|address)|allow_private/.test(m)) return 'private_address';
  if (/certificate|x509|tls/.test(m)) return 'tls';
  if (/timeout|timed out|deadline/.test(m)) return 'timeout';
  if (/rate limit|too many requests/.test(m) || (e instanceof ApiError && e.status === 429)) return 'rate_limited';
  if (/dial |no such host|connection refused|unreachable|couldn't reach|network is/.test(m)) return 'unreachable';
  if (/401|unauthori[sz]ed|invalid_auth|not_authed|invalid token|rejected|bad credentials|forbidden/.test(m)) return 'key_rejected';
  return 'unknown';
}

export function channelFailure(app: AppName, e: unknown): Failure {
  const name = appTitle[app];
  const details = errorText(e);
  switch (failureCode(e)) {
    case 'intent':
      if (app === 'discord') return { message: 'Discord needs Message Content Intent turned on for this bot. Turn it on in the developer portal under Bot, then try again.', details };
      break;
    case 'unreachable':
    case 'timeout':
      return { message: `Bothy couldn't reach ${name} from this server. Check the server's internet connection or firewall.`, details };
    case 'rate_limited':
      return { message: `${name} is limiting requests right now. Wait a minute and try again.`, details };
    case 'unknown':
      return { message: 'Something went wrong while checking. Try again.', details };
  }
  const rejected: Record<AppName, string> = {
    slack: "Slack didn't accept those tokens. Check you copied the app-level token and the bot token from the same app.",
    telegram: "Telegram didn't accept that token. Copy it again from @BotFather.",
    discord: "Discord didn't accept that token. Copy it again from the developer portal.",
  };
  return { message: rejected[app], details };
}

/** The host people know GitHub by on this server: "GitHub", or the Enterprise host. */
export function githubAddress(): string {
  const base = config.value<string>('github.api_base', '');
  try {
    const host = new URL(base).host;
    return !host || host === 'api.github.com' ? 'GitHub' : host;
  } catch {
    return 'GitHub';
  }
}

export type GitHubOutcome = { ok: true; message: string } | (Failure & { ok: false; notInstalled?: boolean });

export function githubOutcome(p: GitHubProbe | null, e: unknown, app: boolean): GitHubOutcome {
  const what = app ? 'app' : 'token';
  if (p?.ok) {
    const usable = p.repos.filter((r) => !r.archived).length;
    if (!usable) return { ok: false, message: `The ${what} works but can't see any repositories. Give it access to at least one repository on GitHub.` };
    const n = plural(usable, 'repository', 'repositories') + (p.truncated ? ' or more' : '');
    return { ok: true, message: app ? `Installed on ${p.account} · ${n}` : `Connected as ${p.account}. Bothy can see ${n}.` };
  }
  const details = p?.error ?? errorText(e);
  if (app && /not installed/i.test(details)) return { ok: false, notInstalled: true, message: "The app isn't installed on any organisation yet.", details };
  const code = failureCode(p ? new Error(details) : e);
  if (code === 'unreachable' || code === 'timeout' || code === 'tls')
    return { ok: false, message: `Bothy couldn't reach ${githubAddress()}. Check the address and that this server can reach it.`, details };
  if (code === 'key_rejected')
    return { ok: false, message: app ? "GitHub didn't accept that app. Check the App ID and the private key." : "GitHub didn't accept that token. It may have expired or been revoked.", details };
  return { ok: false, message: 'Something went wrong while checking. Try again.', details };
}

export type SearchProvider = 'searxng' | 'brave' | 'tavily';

// The model-check codes a search check can produce; the rest read as unknown.
const searchCodes = new Set(['key_rejected', 'out_of_credit', 'rate_limited', 'provider_error', 'timeout', 'unreachable', 'private_address', 'tls']);

export function searchFailure(provider: SearchProvider, code: string, details: string): Failure & { allow?: boolean } {
  const message =
    code === 'not_a_model_server'
      ? "That address answered, but it doesn't look like SearXNG. Check the address."
      : failure(searchCodes.has(code) ? (code as FailureCode) : 'unknown', { name: searchTitle[provider], local: provider === 'searxng' }).text;
  return { message, details, allow: code === 'private_address' };
}

/** The vault name a config field points at ("secret:slack_bot_token"), or the default. */
export function secretName(path: string, fallback: string): string {
  const v = config.value<unknown>(path, '');
  return typeof v === 'string' && v.startsWith('secret:') ? v.slice(7) : fallback;
}

/** Writes config without the v0.1 toasts: errors stay next to what caused them (§14.4). */
export async function saveConfig(set: Record<string, unknown>): Promise<Failure | null> {
  try {
    await api.patch('/api/config', { set });
    await config.load(true);
    return null;
  } catch (e) {
    const fields = e instanceof ApiError ? e.fields.map((f) => f.message).join('\n') : '';
    return { message: "Couldn't save. Try again.", details: fields || errorText(e) };
  }
}

export interface EnvForm {
  name: string;
  repo: string;
  workflow: string;
  branches: string;
  approvers: string[];
  required: number;
  inputs: { key: string; value: string }[];
}

export type EnvField = 'name' | 'repo' | 'workflow' | 'branches' | 'approvers';

export function envErrors(f: EnvForm, taken: string[]): Partial<Record<EnvField, string>> {
  const out: Partial<Record<EnvField, string>> = {};
  if (!f.name) out.name = 'Give the environment a name.';
  else if (!/^[a-z0-9][a-z0-9_-]{0,31}$/.test(f.name)) out.name = 'Use lowercase letters, numbers, - and _, like production.';
  else if (taken.includes(f.name)) out.name = 'There is already an environment with this name.';
  if (!f.repo) out.repo = 'Choose a repository.';
  if (!f.workflow.trim()) out.workflow = 'Choose the workflow Bothy starts.';
  if (!f.branches.trim()) out.branches = 'Enter at least one branch, like main.';
  const roles = f.approvers.filter((a) => !a.startsWith('usr_'));
  if (!f.approvers.length) out.approvers = 'Choose who can approve.';
  else if (f.required === 2 && !roles.length && f.approvers.length < 2) out.approvers = 'Choose at least two people, or Any admin.';
  return out;
}

export function envConfig(f: EnvForm): Record<string, unknown> {
  return {
    name: f.name,
    repo: f.repo,
    workflow: f.workflow.trim(),
    ref_pattern: f.branches
      .split(',')
      .map((b) => b.trim())
      .filter(Boolean)
      .join(','),
    approvers: f.approvers,
    required: f.required,
    inputs: Object.fromEntries(f.inputs.filter((i) => i.key.trim()).map((i) => [i.key.trim(), i.value])),
  };
}

const rank: Record<string, number> = { member: 1, admin: 2, owner: 3 };

/**
 * Whether the chosen approvers can ever meet the number needed: approvers
 * never count for a deploy they asked for themselves (SPEC two-person rule),
 * so a deploy one of them asks for needs `required` others. "" when it can.
 */
export function approverShortfall(approvers: string[], required: number, users: { id: string; role: string; disabled: boolean; display_name: string; username: string }[]): string {
  const active = users.filter((u) => !u.disabled);
  const eligible = active.filter((u) => approvers.some((a) => a === u.id || (rank[a] !== undefined && (rank[u.role] ?? 0) >= rank[a])));
  const n = eligible.length;
  if (n > required) return '';
  if (n < required)
    return n === 0
      ? 'Nobody can approve these deploys yet. Choose who can approve.'
      : `Only ${plural(n, 'person', 'people')} can approve, so a deploy could never get ${required} approvals. Add approvers or ask for fewer.`;
  if (n === 1) {
    const who = eligible[0].display_name || eligible[0].username;
    return `Only ${who} can approve, so a deploy ${who} asks for can't be approved. Add another approver.`;
  }
  return `Only ${n} people can approve. A deploy one of them asks for can't get ${required} approvals from the others. Add another approver or ask for ${required - 1}.`;
}
