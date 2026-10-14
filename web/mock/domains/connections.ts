// Connections (C8): chat apps, pairing, linked accounts, group chats, MCP
// servers, GitHub with its repositories and deploy environments, and the web
// search check. Chat app and MCP state follows the config the settings domain
// keeps, so turning things on and off in the UI shows up in their status.
import type { ChannelIdentity, ChannelStatus, Entry, MCPPin, MCPStatus, SideEffect, Space } from '../../src/lib/api/types.gen.ts';
import type { GitHubConnection, GitHubProbe, GitHubRepo, GitHubRepoAccess, ReleaseEnvironment, Workflow } from '../../src/screens/settings/connections/types.ts';
import { ago, ahead, body, clone, emit, fail, json, member, nowISO, on, onReset, owner, scenario, sleep, ZERO, type Req } from './core.ts';
import { state as security } from './security.ts';
import { state as settings } from './settings.ts';

const offApps = scenario('chat-apps-off', 'Every chat app is turned off; ones with saved tokens read "Off".', 'connections');
const channelDown = scenario('channel-down', "channel-down:<slack|telegram|discord> makes that app's status \"Can't connect\" (rejected token).", 'connections');
const channelFail = scenario('channel-fail', 'Connection tests fail; channel-fail:<slack|telegram|discord|intent> picks the app or Discord intent error.', 'connections');
const paired = scenario('paired', 'paired:<app> links the account a moment after a pairing code is made.', 'connections');
const noTools = scenario('mcp-none', 'No MCP servers are set up.', 'connections');
const installing = scenario('mcp-installing', 'A Sentry MCP server is being installed.', 'connections');
const toolError = scenario('mcp-error', "The Linear MCP server doesn't answer.", 'connections');
const githubNone = scenario('github-none', 'GitHub is not connected.', 'connections');
const githubNoRepos = scenario('github-no-repos', 'GitHub is connected but no repository is chosen.', 'connections');
const githubApp = scenario('github-app', 'GitHub is connected through a GitHub App.', 'connections');
const githubFail = scenario('github-fail', 'GitHub checks fail; github-fail:<rejected|empty|unreachable|not-installed|installations>.', 'connections');
const noWorkflows = scenario('no-workflows', "The repository workflow list isn't available (S19 fallback: a text field).", 'connections');
const noEnvs = scenario('no-environments', 'No deploy environments are set up.', 'connections');
const searchFail = scenario('search-fail', 'The web search check fails; search-fail:<code> picks the S13 code.', 'connections');
const searchPrivate = scenario('search-private', 'SearXNG is on a private address that is not allowed yet.', 'connections');

// ---- config shared with the settings domain ----

const entries = () => settings.effective.entries as Entry[];
const at = (e?: Entry) => (e?.source.at ? Date.parse(e.source.at) : 0);
const cfg = (path: string) => entries().find((e) => e.path === path)?.value;

function seed(path: string, value: unknown) {
  const e = entries().find((x) => x.path === path);
  if (e) e.value = value;
  else entries().push({ path, value, source: { layer: 'file', file: '/etc/bothy/bothy.yaml' }, shadowed: [], locked: false, hot: true, secret: false });
}

/** Items of a config list keyed by name ("mcp.servers.linear.url"), leaving out ones the UI set to null since. */
function items(prefix: string): Map<string, Record<string, unknown>> {
  const out = new Map<string, Record<string, unknown>>();
  const removed = new Map<string, number>();
  for (const e of entries()) {
    if (!e.path.startsWith(prefix + '.')) continue;
    const [name, ...rest] = e.path.slice(prefix.length + 1).split('.');
    if (!rest.length) {
      if (e.value === null) removed.set(name, at(e));
      else out.set(name, { ...(out.get(name) ?? {}), ...(e.value as Record<string, unknown>) });
      continue;
    }
    const item = out.get(name) ?? {};
    item[rest.join('.')] = e.value;
    item['@at'] = Math.max(Number(item['@at'] ?? 0), at(e));
    out.set(name, item);
  }
  for (const [name, when] of removed) if (when >= Number(out.get(name)?.['@at'] ?? 0)) out.delete(name);
  return out;
}

// ---- chat apps ----

const identity: Record<string, string> = { slack: '@bothy', telegram: '@acme_bothy_bot', discord: '@Bothy' };
const tokens: Record<string, string[]> = { slack: ['slack_app_token', 'slack_bot_token'], telegram: ['telegram_bot_token'], discord: ['discord_bot_token'] };
const hasTokens = (n: string) => tokens[n].every((t) => security.secrets.some((s) => s.name === t));

// The Slack workspace name isn't in ChannelStatus yet; it shows the copy §12.1 asks the server for.
function appStatus(req: Req, n: string): ChannelStatus & { workspace?: string } {
  const enabled = cfg(`channels.${n}.enabled`) === true;
  const ok = hasTokens(n);
  const state = !enabled ? 'disabled' : !ok || channelDown.arg(req) === n ? 'auth_failed' : 'connected';
  return {
    name: n,
    enabled,
    state,
    identity: ok ? identity[n] : undefined,
    workspace: n === 'slack' && ok ? 'Acme' : undefined,
    since: ago(60 * 24),
    deep_links: n === 'telegram',
  };
}

export const channels = (req?: Req) => ['slack', 'telegram', 'discord'].map((n) => appStatus(req as Req, n));

export const identities: ChannelIdentity[] = [
  { channel: 'telegram', external_id: '5512', username: 'sam', display_name: 'Sam', status: 'linked', user_id: owner.id, dm_chat_id: '5512', first_seen_at: ago(60 * 24 * 20), last_seen_at: ago(95) },
  { channel: 'slack', external_id: 'U01SAM', username: 'sam', display_name: 'Sam', status: 'linked', user_id: owner.id, dm_chat_id: 'D01SAM', first_seen_at: ago(60 * 24 * 14), last_seen_at: ago(30) },
  { channel: 'slack', external_id: 'U02ALEX', username: 'alex', display_name: 'Alex', status: 'linked', user_id: member.id, dm_chat_id: 'D02ALEX', first_seen_at: ago(60 * 24 * 9), last_seen_at: ago(240) },
  { channel: 'slack', external_id: 'U03JORDAN', username: 'jordan', display_name: 'Jordan Lee', status: 'pending', first_seen_at: ago(300), last_seen_at: ago(300) },
];

const space = (principal_id: string, channel: string, chat_id: string, title: string, enabled: boolean, mention_only: boolean): Space => ({
  principal_id,
  kind: 'group',
  chat: { channel, chat_id, kind: 'group' },
  title,
  owner_user_id: owner.id,
  enabled,
  mention_only,
  created_at: ago(60 * 24 * 9),
  updated_at: ago(60 * 24 * 9),
});

export const spaces: Space[] = [
  space('spc_01jeng00000000000000000000', 'slack', 'C01ENG', '#eng', true, true),
  space('spc_01jsupport0000000000000000', 'slack', 'C02SUP', '#support', false, false),
  space('spc_01jfamily0000000000000000', 'telegram', '-100223', 'Ops team', true, true),
];

// ---- MCP servers ----

const pin = (server: string, tool: string, side_effect: SideEffect, description: string, status = 'approved', hash = 'a1'): MCPPin => ({
  server,
  tool,
  hash,
  definition: { name: tool, description, inputSchema: { type: 'object', properties: { query: { type: 'string' } } } },
  status,
  side_effect,
  private: false,
  first_seen_at: ago(60 * 24 * 5),
  approved_at: status === 'pending' ? ZERO : ago(60 * 24 * 5),
});

const linearTools: [string, SideEffect, string][] = [
  ['list_issues', 'read', 'List issues in a team, filtered by state, assignee or label.'],
  ['get_issue', 'read', 'Read one issue with its comments.'],
  ['search_issues', 'read', 'Search issues by text.'],
  ['list_projects', 'read', 'List projects and their progress.'],
  ['list_teams', 'read', 'List teams in the workspace.'],
  ['list_cycles', 'read', "List a team's cycles."],
  ['create_issue', 'external_send', 'Create an issue in a team.'],
  ['update_issue', 'external_send', "Change an issue's title, description, state or priority."],
  ['assign_issue', 'external_send', 'Assign an issue to someone.'],
  ['add_comment', 'external_send', 'Add a comment to an issue.'],
  ['close_issue', 'external_send', 'Close an issue as done or cancelled.'],
  ['delete_comment', 'irreversible', 'Delete a comment.'],
];

export const mcp: MCPStatus[] = [
  { name: 'docs', transport: 'http', state: 'pins_pending', protocol: '2026-07-28', tools: 3, pending: 1, since: ago(60) },
  { name: 'linear', transport: 'http', state: 'ready', protocol: '2026-07-28', tools: 12, pending: 0, since: ago(60 * 5) },
  { name: 'notes', transport: 'stdio', state: 'disabled', protocol: '', tools: 0, pending: 0, since: ago(60 * 24) },
];

export const mcpPins: MCPPin[] = [
  { ...pin('docs', 'search_docs', 'read', 'Search the team handbook and return matching sections.', 'pending', 'b3c1'), first_seen_at: ago(60) },
  {
    ...pin('docs', 'search_docs', 'read', 'Search the team handbook.', 'superseded', 'a9f0'),
    definition: { name: 'search_docs', description: 'Search the team handbook.', inputSchema: { type: 'object', properties: { query: { type: 'string' } } } },
  },
  pin('docs', 'get_page', 'read', 'Read one handbook page.'),
  pin('docs', 'list_spaces', 'read', 'List the handbook spaces.'),
  ...linearTools.map(([t, se, d]) => pin('linear', t, se, d, t === 'delete_comment' ? 'disabled' : 'approved')),
];

// ---- GitHub ----

let githubId = 7_100_000;
const access = (name: string, description: string, archived = false): GitHubRepoAccess => ({
  github_id: ++githubId,
  owner: 'acme',
  name,
  private: true,
  archived,
  default_branch: 'main',
  description,
  allowed: false,
});

export const githubRepos: GitHubRepoAccess[] = [
  access('api', 'Go API'),
  access('design-system', 'Shared UI components'),
  access('docs', 'Handbook and runbooks'),
  access('infra', 'Terraform for production and staging'),
  access('legacy-billing', 'Old billing service', true),
  access('mobile', 'iOS and Android apps'),
  access('web', 'Customer web app'),
];

const CONN = 'ghc_01jacme00000000000000000000';

function connection(app: boolean): GitHubConnection {
  return {
    id: CONN,
    auth: app ? 'app' : 'pat',
    account: app ? 'acme' : 'acme-bot',
    account_type: app ? 'Organization' : 'User',
    app_id: app ? '123456' : undefined,
    app_slug: app ? 'acme-bothy' : undefined,
    installation_id: app ? 4242 : undefined,
    secret_ref: app ? 'secret:github_app_key' : 'secret:github_token',
    owns_secret: true,
    status: 'ok',
    token_expires_at: app ? ZERO : ahead(60 * 24 * 80),
    checked_at: ago(30),
    created_by: owner.id,
    created_at: ago(60 * 24 * 3),
    updated_at: ago(30),
  };
}

export const state = {
  identities: [] as ChannelIdentity[],
  spaces: [] as Space[],
  mcp: [] as MCPStatus[],
  pins: [] as MCPPin[],
  github: null as GitHubConnection | null,
  held: [] as GitHubRepo[],
  madeHere: false,
};

const held = (name: string, allowed: boolean): GitHubRepo => ({
  connection_id: CONN,
  github_id: githubRepos.find((r) => r.name === name)?.github_id ?? 0,
  owner: 'acme',
  name,
  default_branch: 'main',
  private: true,
  allowed,
  created_at: ago(60 * 24 * 3),
  updated_at: ago(60 * 24 * 3),
});

onReset(() => {
  Object.assign(state, {
    identities: clone(identities),
    spaces: clone(spaces),
    mcp: clone(mcp),
    pins: clone(mcpPins),
    github: connection(false),
    held: [held('web', true), held('api', true), held('infra', false)],
    madeHere: false,
  });
  seed('channels.slack.enabled', true);
  seed('channels.telegram.enabled', true);
  seed('channels.slack.app_token', 'secret:slack_app_token');
  seed('channels.slack.bot_token', 'secret:slack_bot_token');
  for (const name of ['slack_app_token', 'slack_bot_token'])
    if (!security.secrets.some((s) => s.name === name)) security.secrets.push({ scope: 'global', name, source: 'vault', hint: 'q7Lm', updated_at: ago(60 * 24 * 14), last_used_at: ago(2) });
  for (const [k, v] of Object.entries({ name: 'linear', transport: 'http', url: 'https://mcp.linear.app/mcp' })) seed(`mcp.servers.linear.${k}`, v);
  for (const [n, e] of Object.entries({
    production: { name: 'production', repo: 'acme/web', workflow: 'deploy.yml', ref_pattern: 'main', approvers: [member.id, 'admin'], required: 2, inputs: {} },
    staging: { name: 'staging', repo: 'acme/web', workflow: 'deploy.yml', ref_pattern: 'main', approvers: ['admin'], required: 1, inputs: {} },
  }))
    for (const [k, v] of Object.entries(e)) seed(`releases.environments.${n}.${k}`, v);
});

// ---- routes: chat apps, pairing, identities, group chats ----

on('GET', '/api/channels', (req, res) => {
  const list = channels(req);
  json(res, 200, offApps(req) ? list.map((c) => ({ ...c, enabled: false, state: 'disabled', identity: undefined, workspace: undefined })) : list);
});

const rawFailure: Record<string, string> = {
  slack: 'slack: auth.test: invalid_auth',
  telegram: 'telegram: getMe: 401 Unauthorized',
  discord: 'discord: GET /users/@me: 401 Unauthorized',
  intent: 'discord: gateway closed with code 4014: Disallowed intent(s)',
};

on('POST', '/api/channels/{name}/test', async (req, res, m) => {
  await sleep(400);
  const f = channelFail.arg(req);
  if (f !== undefined && (!f || f === m[1] || (f === 'intent' && m[1] === 'discord'))) return fail(res, 502, 'bad_gateway', rawFailure[f === 'intent' ? 'intent' : m[1]]);
  json(res, 200, { identity: identity[m[1]] ?? '@bothy' });
});
on('GET', '/api/channels/slack/manifest', (_q, res) => {
  res.setHeader('Content-Type', 'text/yaml');
  res.end('display_information:\n  name: "Bothy"\nsettings:\n  socket_mode_enabled: true\n');
});
on('GET', '/api/channels/discord/invite', (_q, res) => json(res, 200, { url: 'https://discord.com/oauth2/authorize?client_id=123&scope=bot%20applications.commands&permissions=274877975552' }));

on('POST', '/api/pairing', (req, res) => {
  const app = paired.arg(req);
  const linked = app ? { channel: app, external_id: 'U01SAM', display: '@sam' } : { channel: 'telegram', external_id: '5512', display: '@sam' };
  setTimeout(() => emit('pairing.linked', linked), app ? 800 : 6000);
  json(res, 200, { code: '7KQ2MX4D', expires_at: new Date(Date.now() + 15 * 60_000).toISOString(), deep_links: { telegram: 'https://t.me/acme_bothy_bot?start=7KQ2MX4D' } });
});
on('POST', '/api/pairing/approve', (_q, res) => json(res, 200, state.identities[3]));
on('GET', '/api/identities', (_q, res) => json(res, 200, state.identities));
on('PATCH', '/api/identities/{channel}/{external}', async (req, res, m) => {
  const i = state.identities.find((x) => x.channel === m[1] && x.external_id === m[2]);
  if (i) Object.assign(i, JSON.parse(await body(req)));
  json(res, 200, i);
});
on('DELETE', '/api/identities/{channel}/{external}', (_q, res, m) => {
  state.identities = state.identities.filter((x) => !(x.channel === m[1] && x.external_id === m[2]));
  json(res, 204);
});
on('GET', '/api/spaces', (_q, res) => json(res, 200, state.spaces));
on('PATCH', '/api/spaces/{id}', async (req, res, m) => {
  const s = state.spaces.find((x) => x.principal_id === m[1]);
  if (s) Object.assign(s, JSON.parse(await body(req)));
  json(res, 200, s);
});

// ---- routes: MCP ----

// Servers come from config, so ones added or removed in the UI appear and go.
function servers(req: Req): MCPStatus[] {
  if (noTools(req)) return [];
  const extra: MCPStatus[] = installing(req) ? [{ name: 'sentry', transport: 'stdio', state: 'installing', protocol: '', tools: 0, pending: 0, since: nowISO() }] : [];
  return [...items('mcp.servers')].map(([name, c]) => {
    const s = state.mcp.find((x) => x.name === name);
    if (s && name === 'linear' && toolError(req)) return { ...s, state: 'error', tools: 0, error: 'POST https://mcp.linear.app/mcp: 502 Bad Gateway' };
    if (s) return { ...s, state: c.enabled === false ? 'disabled' : s.state === 'disabled' && c.enabled === true ? 'ready' : s.state };
    return { name, transport: String(c.transport ?? 'http'), state: c.transport === 'stdio' ? 'installing' : 'connecting', protocol: '', tools: 0, pending: 0, since: nowISO() };
  }).concat(extra);
}

on('GET', '/api/mcp', (req, res) => json(res, 200, servers(req)));
on('GET', '/api/mcp/{server}/pins', (_q, res, m) => json(res, 200, state.pins.filter((p) => p.server === m[1])));
on('POST', '/api/mcp/{server}/approve', async (req, res, m) => {
  const { tools } = JSON.parse(await body(req)) as { tools: Record<string, { side_effect: SideEffect; disable?: boolean }> };
  for (const [tool, d] of Object.entries(tools)) {
    const mine = state.pins.filter((p) => p.server === m[1] && p.tool === tool);
    const latest = mine.find((p) => p.status === 'pending') ?? mine.find((p) => p.status === 'approved' || p.status === 'disabled');
    if (!latest) continue;
    for (const p of mine) if (p !== latest && p.status !== 'superseded') p.status = 'superseded';
    Object.assign(latest, { status: d.disable ? 'disabled' : 'approved', side_effect: d.side_effect, approved_at: nowISO() });
  }
  const s = state.mcp.find((x) => x.name === m[1]);
  if (s) {
    s.pending = state.pins.filter((p) => p.server === m[1] && p.status === 'pending').length;
    if (!s.pending && s.state === 'pins_pending') s.state = 'ready';
  }
  json(res, 204);
});
on('POST', '/api/mcp/{server}/install', (_q, res) => json(res, 202));
on('POST', '/api/mcp/{server}/reload', (_q, res) => json(res, 202));

// ---- routes: GitHub (F4 API on the server) ----

function probe(req: Req, auth: string, connected: boolean, installation?: number): GitHubProbe {
  const kind = githubFail.arg(req);
  const app = auth === 'app';
  const base: GitHubProbe = {
    ok: true,
    auth,
    account: app ? 'acme' : connected ? (state.github?.account ?? 'acme-bot') : 'maya-acme',
    account_type: app ? 'Organization' : 'User',
    app_slug: app ? 'acme-bothy' : undefined,
    installation_id: app ? (installation ?? 4242) : undefined,
    repos: githubRepos.map((r) => {
      const h = connected ? state.held.find((x) => x.name === r.name) : undefined;
      return { ...r, allowed: !!h?.allowed, connection_id: h ? CONN : undefined };
    }),
    truncated: false,
    token_expires_at: app ? ZERO : ahead(60 * 24 * 80),
    rate_limit: { limit: 5000, remaining: 4812, reset: ahead(40) },
  };
  switch (kind) {
    case 'rejected':
      return { ...base, ok: false, repos: [], error: 'GitHub rejected the credentials (wrong, expired or revoked token)' };
    case 'empty':
      return { ...base, repos: [] };
    case 'unreachable':
      return { ...base, ok: false, repos: [], error: 'Get "https://api.github.com/user": dial tcp: lookup api.github.com: no such host' };
    case 'not-installed':
      return app ? { ...base, ok: false, repos: [], error: 'the App is not installed on any account yet: install it on GitHub first' } : base;
    case 'installations':
      return app && !installation
        ? {
            ...base,
            ok: false,
            repos: [],
            installations: [
              { id: 4242, account: 'acme', account_type: 'Organization', repository_selection: 'all' },
              { id: 4343, account: 'acme-labs', account_type: 'Organization', repository_selection: 'selected' },
            ],
            error: 'the App has 2 installations: choose one',
          }
        : base;
  }
  return base;
}

// github-none starts disconnected; a connection made in the UI then sticks.
const connected = (req: Req) => (githubNone(req) && !state.madeHere ? null : state.github && { ...state.github, ...(githubApp(req) && !state.madeHere ? connection(true) : {}) });

on('GET', '/api/github/connections', (req, res) => {
  const c = connected(req);
  json(res, 200, c ? [c] : []);
});
on('POST', '/api/github/connections/test', async (req, res) => {
  const b = JSON.parse(await body(req));
  await sleep(400);
  const token = String(b.token ?? '');
  const forced = token.includes('bad') ? 'rejected' : token.includes('empty') ? 'empty' : '';
  const p = probe(req, b.auth, false, b.installation_id);
  json(res, 200, forced === 'rejected' ? { ...p, ok: false, repos: [], error: 'GitHub rejected the credentials (wrong, expired or revoked token)' } : forced ? { ...p, repos: [] } : p);
});
on('POST', '/api/github/connections', async (req, res) => {
  const b = JSON.parse(await body(req));
  state.github = { ...connection(b.auth === 'app'), account: b.auth === 'app' ? 'acme' : 'maya-acme', created_at: nowISO(), checked_at: nowISO() };
  state.madeHere = true;
  state.held = (b.repos as string[]).map((r) => held(r.split('/')[1], true));
  json(res, 200, state.github);
});
on('GET', '/api/github/connections/{id}', (req, res) => {
  const c = connected(req);
  if (!c) return fail(res, 404, 'not_found', 'no such GitHub connection');
  json(res, 200, { connection: c, repos: githubNoRepos(req) && !state.madeHere ? state.held.map((h) => ({ ...h, allowed: false })) : state.held });
});
on('POST', '/api/github/connections/{id}/test', (req, res) => json(res, 200, probe(req, state.github?.auth ?? 'pat', true)));
on('PUT', '/api/github/connections/{id}/repos', async (req, res) => {
  const want = (JSON.parse(await body(req)).repos as string[]).map((r) => r.split('/')[1]);
  for (const h of state.held) h.allowed = want.includes(h.name);
  for (const name of want) if (!state.held.some((h) => h.name === name)) state.held.push(held(name, true));
  json(res, 200, state.held);
});
on('DELETE', '/api/github/connections/{id}', (_q, res) => {
  state.github = null;
  state.held = [];
  json(res, 204);
});
on('GET', '/api/github/repos', (req, res) => json(res, 200, connected(req) && !(githubNoRepos(req) && !state.madeHere) ? state.held.filter((h) => h.allowed) : []));
// S19 asks the server for this list; the dialog falls back to a text field without it.
on('GET', '/api/github/repos/{owner}/{repo}/workflows', (req, res) => {
  if (noWorkflows(req)) return fail(res, 404, 'not_found', 'mock: no workflow list');
  const list: Workflow[] = [
    { name: 'CI', path: '.github/workflows/ci.yml' },
    { name: 'Deploy', path: '.github/workflows/deploy.yml' },
  ];
  json(res, 200, list);
});

/** The deploy environments as GET /api/releases lists them, read from config so the dialog's edits show; the activity domain serves the route. */
export function releaseEnvironments(req: Req): ReleaseEnvironment[] {
  const people = settings.users;
  // no-environments hides the seeded ones; environments added in the UI (they carry a time) still show.
  return [...items('releases.environments')]
    .filter(([, e]) => !noEnvs(req) || Number(e['@at']) > 0)
    .map(([name, e]) => ({
        name,
        repo: String(e.repo ?? ''),
        workflow: String(e.workflow ?? ''),
        ref_pattern: String(e.ref_pattern ?? ''),
        inputs: (e.inputs as Record<string, string>) ?? {},
        required: Number(e.required ?? 1),
        approvers: ((e.approvers as string[]) ?? []).map((id) => {
          const u = people.find((p) => p.id === id);
          return u ? { kind: 'user', id, name: u.display_name } : { kind: 'role', id, name: id };
        }),
      }));
}

// ---- routes: web search check (not on the server yet) ----

const privateHost = (u: string) => {
  try {
    const h = new URL(u).hostname;
    return !h.includes('.') || /^(10\.|127\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(h) || h === 'localhost';
  } catch {
    return false;
  }
};

on('POST', '/api/search/test', async (req, res) => {
  const b = JSON.parse(await body(req));
  await sleep(400);
  const code = searchFail.arg(req);
  if (code !== undefined) return json(res, 200, { ok: false, code: code || 'unreachable', error: `search: ${code || 'unreachable'}: mock failure` });
  const allowed = entries().find((e) => e.path === 'search.searxng_allow_private');
  if (b.provider === 'searxng' && privateHost(b.searxng_url) && (searchPrivate(req) ? allowed?.source.layer !== 'ui' : allowed?.value !== true))
    return json(res, 200, { ok: false, code: 'private_address', error: `egress: ${new URL(b.searxng_url).host} is a private address (search.searxng_allow_private is off)` });
  if (String(b.api_key ?? '').includes('bad')) return json(res, 200, { ok: false, code: 'key_rejected', error: `${b.provider}: 401 Unauthorized: invalid subscription token` });
  json(res, 200, { ok: true });
});
