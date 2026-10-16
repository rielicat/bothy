// Connections (C8): chat apps, the people Bothy answers in them, group chats,
// MCP servers, GitHub with its repositories and deploy environments, and the
// web search check. Chat app and MCP state follows the config the settings
// domain keeps, so turning things on and off in the UI shows up in their status.
import type { ChannelIdentity, ChannelStatus, Entry, MCPPin, MCPStatus, SideEffect, SpaceView, Teammate } from '../../src/lib/api/types.gen.ts';
import type { GitHubApp, GitHubConnection, GitHubProbe, GitHubRepo, GitHubRepoAccess, ReleaseEnvironment, Workflow } from '../../src/screens/settings/connections/types.ts';
import { ago, ahead, body, clone, emit, fail, json, member, nowISO, on, onReset, owner, scenario, sleep, ZERO, type Req } from './core.ts';
import { state as security } from './security.ts';
import { state as settings } from './settings.ts';

const offApps = scenario('chat-apps-off', 'Every chat app is turned off; ones with saved tokens read "Off".', 'connections');
const channelDown = scenario('channel-down', "channel-down:<slack|telegram|discord> makes that app's status \"Can't connect\" (rejected token).", 'connections');
const channelFail = scenario('channel-fail', 'Connection tests fail; channel-fail:<slack|telegram|discord|intent> picks the app or Discord intent error.', 'connections');
const discordOn = scenario('discord-on', "Discord is connected too; discord-on:<no-access|idle> adds that status: it can't check members, or sits in a server with no channel on.", 'connections');
const nobody = scenario('chat-people-none', 'Nobody has messaged Bothy in a chat app.', 'connections');
const noTools = scenario('mcp-none', 'No MCP servers are set up.', 'connections');
const installing = scenario('mcp-installing', 'A Sentry MCP server is being installed.', 'connections');
const toolError = scenario('mcp-error', "The Linear MCP server doesn't answer.", 'connections');
const githubNone = scenario('github-none', 'GitHub is not connected.', 'connections');
const githubNoRepos = scenario('github-no-repos', 'GitHub is connected but no repository is chosen.', 'connections');
const githubApp = scenario('github-app', 'GitHub is connected through a GitHub App.', 'connections');
const githubFail = scenario('github-fail', 'GitHub checks fail; github-fail:<rejected|empty|unreachable|not-installed|installations>.', 'connections');
const appPending = scenario('github-app-pending', 'GitHub is not connected; the app Bothy created, Bothy (acme), is not installed yet.', 'connections');
const appInstalls = scenario('github-app-installs', 'Like github-app-pending, but Check again finds the app installed.', 'connections');
const appMade = scenario('github-app-made', 'GitHub is connected through the app Bothy created, Bothy (acme).', 'connections');
const appBroken = scenario('github-app-broken', 'GitHub stopped accepting Bothy (acme), the app Bothy created: it was uninstalled.', 'connections');
const appRetired = scenario('github-app-retired', 'GitHub is not connected; Bothy no longer uses Bothy (acme), still on GitHub.', 'connections');
const appStartFail = scenario('github-app-start-fail', 'Creating the app fails to start; github-app-start-fail:<wrong_address|sealed|conflict>.', 'connections');
const noWorkflows = scenario('no-workflows', "GitHub can't list the repository's workflows, so the dialog takes a file name.", 'connections');
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

// Discord's member check (channel/discord) and idle servers (channel.Hub.Status); detail is the server's own sentence.
const discordDetail: Record<string, string> = { 'no-access': "can't check who is in the server", idle: "Bothy is in 2 servers where it isn't on in any channel." };

function appStatus(req: Req, n: string): ChannelStatus {
  const enabled = cfg(`channels.${n}.enabled`) === true || (n === 'discord' && discordOn(req));
  const ok = hasTokens(n) || (n === 'discord' && discordOn(req));
  const state = !enabled ? 'disabled' : !ok || channelDown.arg(req) === n ? 'auth_failed' : 'connected';
  const out: ChannelStatus = { name: n, enabled, state, identity: ok ? identity[n] : undefined, team: n === 'slack' && ok ? 'Acme' : undefined, since: ago(60 * 24) };
  if (n === 'discord' && state === 'connected') {
    const arg = discordOn.arg(req) ?? '';
    Object.assign(out, { detail: discordDetail[arg], member_check: arg === 'no-access' ? 'no_access' : 'ok', idle_servers: arg === 'idle' ? 2 : 0 });
  }
  return out;
}

export const channels = (req?: Req) => ['slack', 'telegram', 'discord'].map((n) => appStatus(req as Req, n));

// People in chat apps (internal/auth teammates): Sam and Alex are members of
// the Slack workspace, Morgan of the Discord server, Riley has left Acme,
// Casey is blocked and Jordan waits on Telegram, which can't say who is in the team.
const person = (channel: string, external_id: string, display_name: string, username: string, extra: Partial<ChannelIdentity>): ChannelIdentity => ({
  channel,
  external_id,
  username,
  display_name,
  status: 'teammate',
  admitted_via: 'membership',
  can_approve: false,
  membership: 'member',
  membership_at: ago(20),
  dm_chat_id: external_id,
  first_seen_at: ago(60 * 24 * 14),
  last_seen_at: ago(240),
  ...extra,
});

export const PEOPLE = { sam: 'spc_01jsamslack0000000000000000', alex: 'spc_01jalexslack000000000000000', morgan: 'spc_01jmorgandisc00000000000000', riley: 'spc_01jrileyslack00000000000000', casey: 'spc_01jcaseytg000000000000000000' };

export const identities: ChannelIdentity[] = [
  person('slack', 'U01SAM', 'Sam', 'sam', { principal_id: PEOPLE.sam, can_approve: true, dm_chat_id: 'D01SAM', last_seen_at: ago(30) }),
  person('slack', 'U02ALEX', 'Alex Kim', 'alexk', { principal_id: PEOPLE.alex, dm_chat_id: 'D02ALEX' }),
  person('discord', '1188221100445566', 'Morgan Reyes', 'morgan', { principal_id: PEOPLE.morgan, last_seen_at: ago(60 * 26) }),
  person('slack', 'U04RILEY', 'Riley Park', 'riley', { principal_id: PEOPLE.riley, membership: 'outsider', dm_chat_id: 'D04RILEY', last_seen_at: ago(60 * 24 * 6) }),
  person('telegram', '8830', 'Casey', '', { principal_id: PEOPLE.casey, status: 'blocked', admitted_via: 'admin', membership: 'unknown', last_seen_at: ago(60 * 24 * 3) }),
  person('telegram', '7215', 'Jordan Lee', 'jordan_l', {
    status: 'pending',
    admitted_via: '',
    membership: 'unknown',
    first_seen_at: ago(300),
    last_seen_at: ago(300),
    first_text: 'Hi, I run the Edinburgh office. Can Bothy send me the weekly sales summary too?',
  }),
];

// What each person in a chat app spent today, by principal.
const spentToday: Record<string, number> = { [PEOPLE.sam]: 420_000, [PEOPLE.alex]: 1_150_000, [PEOPLE.morgan]: 80_000 };

const space = (principal_id: string, channel: string, chat_id: string, title: string, extra: Partial<SpaceView>): SpaceView => ({
  principal_id,
  kind: 'group',
  chat: { channel, chat_id, kind: 'group' },
  title,
  members: 0,
  external: false,
  enabled: true,
  mention_only: true,
  tool_profile: 'restricted',
  reviewed_at: ago(60 * 24 * 9),
  created_at: ago(60 * 24 * 9),
  updated_at: ago(60 * 24 * 9),
  ...extra,
});

export const spaces: SpaceView[] = [
  space('spc_01jeng00000000000000000000', 'slack', 'C01ENG', '#eng', { team_name: 'Acme', members: 24, tool_profile: 'standard' }),
  // New since an admin last looked: both wait in Approvals.
  space('spc_01jsupport0000000000000000', 'slack', 'C02SUP', '#support', {
    team_name: 'Acme',
    members: 57,
    external: true,
    enabled: false,
    mention_only: false,
    added_by: 'U01SAM',
    reviewed_at: ZERO,
    created_at: ago(90),
    updated_at: ago(90),
  }),
  space('spc_01jcommunity00000000000000', 'discord', '1300000000000000001', '#general', { team_id: '1290000000000000001', team_name: 'Acme Community', members: 4812, enabled: false, reviewed_at: ZERO, created_at: ago(45), updated_at: ago(45) }),
  space('spc_01jfamily0000000000000000', 'telegram', '-100223', 'Ops team', { members: 6 }),
];

const name = (i: ChannelIdentity) => i.display_name || (i.username ? `@${i.username}` : 'Someone');

/** As httpapi teammate() sends it. */
export function teammate(i: ChannelIdentity): Teammate {
  return {
    channel: i.channel,
    external_id: i.external_id,
    name: name(i),
    handle: i.username || undefined,
    status: i.status,
    can_approve: i.can_approve,
    left: i.membership === 'outsider',
    last_seen_at: i.last_seen_at,
    spent_today_micros: spentToday[i.principal_id ?? ''] ?? 0,
    principal_id: i.principal_id,
  };
}

/** The identity's name for an InboxGroup's added_by. */
export const addedBy = (sp: SpaceView): string => {
  const i = state.identities.find((x) => x.channel === sp.chat.channel && x.external_id === sp.added_by);
  return i ? name(i) : '';
};

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

const APP = 'gha_01jacme00000000000000000000';

/** The app Bothy created for acme, in one of its states (§12.2). */
function madeApp(st: 'pending' | 'connected' | 'retired'): GitHubApp {
  return {
    id: APP,
    app_id: 912345,
    slug: 'bothy-acme',
    name: 'Bothy (acme)',
    owner: 'acme',
    owner_id: 1284731,
    owner_type: 'Organization',
    client_id: 'Iv23liAbCdEfGhIj',
    key_ref: st === 'retired' ? '' : `secret:github_${APP}_key`,
    client_secret_ref: st === 'retired' ? '' : `secret:github_${APP}_client_secret`,
    connection_id: st === 'connected' ? CONN : undefined,
    retired_at: st === 'retired' ? ago(30) : undefined,
    created_by: owner.id,
    created_at: ago(60 * 24),
    updated_at: ago(30),
    state: st,
    html_url: 'https://github.com/apps/bothy-acme',
    install_url: 'https://github.com/apps/bothy-acme/installations/new',
    settings_url: 'https://github.com/organizations/acme/settings/apps/bothy-acme',
  };
}

/** A connection through the app Bothy created. */
function appConnection(): GitHubConnection {
  return { ...connection(true), app_id: '912345', app_slug: 'bothy-acme', installation_id: 5151, secret_ref: `secret:github_${APP}_key`, checked_at: nowISO() };
}

export const state = {
  identities: [] as ChannelIdentity[],
  spaces: [] as SpaceView[],
  mcp: [] as MCPStatus[],
  pins: [] as MCPPin[],
  github: null as GitHubConnection | null,
  held: [] as GitHubRepo[],
  madeHere: false,
  // Apps Bothy created; scenarios seed them until the UI changes one.
  apps: [] as GitHubApp[],
  appsChanged: false,
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
    apps: [],
    appsChanged: false,
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

// ---- routes: chat apps, people in them, group chats ----

on('GET', '/api/channels', (req, res) => {
  const list = channels(req);
  json(res, 200, offApps(req) ? list.map((c) => ({ ...c, enabled: false, state: 'disabled', identity: undefined, team: undefined })) : list);
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

on('GET', '/api/identities', (req, res) => json(res, 200, nobody(req) ? [] : state.identities));
const appWord: Record<string, string> = { slack: 'Slack', telegram: 'Telegram', discord: 'Discord' };
// auth.SetTeammate's rules and refusals.
on('PATCH', '/api/identities/{channel}/{external}', async (req, res, m) => {
  const i = state.identities.find((x) => x.channel === m[1] && x.external_id === m[2]);
  if (!i) return fail(res, 404, 'not_found', 'not found');
  const b = JSON.parse(await body(req)) as { status?: string; can_approve?: boolean };
  if (b.status === 'teammate' && i.status === 'pending') {
    if (i.membership === 'outsider') return fail(res, 400, 'bad_request', `${appWord[i.channel]} says they aren't in the team.`);
    if (i.membership !== 'unknown')
      return fail(res, 400, 'bad_request', `${appWord[i.channel]} decides who can use Bothy there. They get in when they message Bothy, if they're in the team.`);
    Object.assign(i, { status: 'teammate', admitted_via: 'admin', principal_id: i.principal_id ?? `spc_01j${i.external_id.padEnd(23, '0')}`, first_text: '' });
  } else if (b.status === 'teammate') i.status = 'teammate';
  else if (b.status === 'blocked') Object.assign(i, { status: 'blocked', can_approve: false });
  else if (b.status) return fail(res, 400, 'bad_request', 'status must be teammate or blocked');
  if (b.can_approve !== undefined) {
    if (b.can_approve && i.status !== 'teammate') return fail(res, 400, 'bad_request', 'Let them in first.');
    if (b.can_approve && i.membership === 'outsider') return fail(res, 400, 'bad_request', "They aren't in the team any more.");
    i.can_approve = b.can_approve;
  }
  emit('inbox.changed', { item: `sender:${i.channel}:${i.external_id}` });
  // A change in People retires the 0013 report (teamAccessReviewed).
  emit('banner', { level: 'info', code: 'team_access', text: '', clear: true });
  json(res, 200, teammate(i));
});
// Approvers first, then by when they were last active, then blocked people.
on('GET', '/api/teammates', (req, res) => {
  const rank = (i: ChannelIdentity) => (i.status === 'blocked' ? 2 : i.can_approve ? 0 : 1);
  const list = nobody(req) ? [] : state.identities.filter((i) => i.status === 'teammate' || i.status === 'blocked');
  json(res, 200, list.sort((a, b) => rank(a) - rank(b) || b.last_seen_at.localeCompare(a.last_seen_at)).map(teammate));
});
on('DELETE', '/api/teammates/{channel}/{external}', (_q, res, m) => {
  const i = state.identities.find((x) => x.channel === m[1] && x.external_id === m[2]);
  if (!i) return fail(res, 404, 'not_found', 'not found');
  if (i.status !== 'blocked' && i.membership !== 'outsider') return fail(res, 400, 'bad_request', 'Block them first.');
  Object.assign(i, { status: i.status === 'blocked' ? 'blocked' : 'pending', principal_id: undefined, admitted_via: '', can_approve: false });
  json(res, 204);
});
on('GET', '/api/spaces', (_q, res) => json(res, 200, state.spaces));
on('PATCH', '/api/spaces/{id}', async (req, res, m) => {
  const s = state.spaces.find((x) => x.principal_id === m[1]);
  if (!s) return fail(res, 404, 'not_found', 'not found');
  const b = JSON.parse(await body(req));
  Object.assign(s, b, 'enabled' in b ? { reviewed_at: nowISO() } : {});
  json(res, 200, s);
});
on('DELETE', '/api/spaces/{id}', (_q, res, m) => {
  const s = state.spaces.find((x) => x.principal_id === m[1]);
  if (!s) return fail(res, 404, 'not_found', 'not found');
  if (s.enabled) return fail(res, 400, 'bad_request', 'Turn it off first.');
  state.spaces = state.spaces.filter((x) => x !== s);
  emit('inbox.changed', { item: `group:${s.principal_id}` });
  json(res, 204);
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
on('POST', '/api/mcp/resolve', async (req, res) => {
  const b = JSON.parse(await body(req)) as { name?: string; version?: string };
  if (!b.name || b.name.includes('missing')) return fail(res, 404, 'not_found', `npm has no package called ${b.name ?? ''}.`);
  json(res, 200, { name: b.name, version: b.version || '1.4.2', integrity: 'sha512-mockIntegrityHash==' });
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

// github-none and the app scenarios without a connection start disconnected;
// a connection made in the UI then sticks.
function connected(req: Req): GitHubConnection | null {
  if (state.madeHere) return state.github;
  if (githubNone(req) || appPending(req) || appInstalls(req) || appRetired(req)) return null;
  if (appMade(req)) return appConnection();
  if (appBroken(req)) return { ...appConnection(), status: 'error', error: 'GitHub rejected the credentials (wrong, expired or revoked token)' };
  return state.github && { ...state.github, ...(githubApp(req) ? connection(true) : {}) };
}

function appsFor(req: Req): GitHubApp[] {
  if (state.appsChanged) return state.apps;
  if (appPending(req) || appInstalls(req)) return [madeApp('pending')];
  if (appMade(req) || appBroken(req)) return [madeApp('connected')];
  if (appRetired(req)) return [madeApp('retired')];
  return [];
}

function setApps(apps: GitHubApp[]) {
  state.apps = apps;
  state.appsChanged = true;
}

// The app's installation connected: no repository is chosen yet, so the page opens the picker.
function connectApp(): GitHubConnection {
  state.github = appConnection();
  state.madeHere = true;
  state.held = [];
  setApps([madeApp('connected')]);
  return state.github;
}

// ---- routes: Bothy's own GitHub App (h_github_app.go) ----

on('GET', '/api/github/apps', (req, res) => json(res, 200, appsFor(req)));
on('POST', '/api/github/apps', async (req, res) => {
  await body(req);
  switch (appStartFail.arg(req)) {
    case 'wrong_address':
      return fail(res, 409, 'wrong_address', 'open Bothy at https://bothy.acme.dev to create the GitHub App', 'https://bothy.acme.dev');
    case 'sealed':
      return fail(res, 503, 'sealed', "Bothy's saved keys are locked.", '/settings/security/keys');
    case 'conflict':
      setApps([madeApp('pending')]);
      return fail(res, 409, 'conflict', 'finish installing Bothy (acme) on GitHub, or start over');
  }
  json(res, 200, { handoff_url: '/api/github/apps/handoff' });
});
// Mock only: stands in for GitHub's create and install pages and the two redirects back.
on('GET', '/api/github/apps/handoff', (_q, res) => {
  connectApp();
  res.statusCode = 303;
  res.setHeader('Location', '/settings/connections/github?github_app=installed');
  res.end();
});
on('POST', '/api/github/apps/{id}/check', async (req, res) => {
  await sleep(300);
  if (appInstalls(req) && appsFor(req).some((a) => a.state === 'pending')) return json(res, 200, { installed: true, connection: connectApp() });
  json(res, 200, { installed: false });
});
on('POST', '/api/github/apps/{id}/retire', (req, res, m) => {
  const a = appsFor(req).find((x) => x.id === m[1]);
  if (!a) return fail(res, 404, 'not_found', 'not found');
  if (a.state === 'connected') return fail(res, 409, 'conflict', 'disconnect GitHub first');
  const r = madeApp('retired');
  setApps(appsFor(req).map((x) => (x.id === m[1] ? r : x)));
  json(res, 200, r);
});
on('DELETE', '/api/github/apps/{id}', (req, res, m) => {
  const a = appsFor(req).find((x) => x.id === m[1]);
  if (!a) return fail(res, 404, 'not_found', 'not found');
  if (a.state !== 'retired') return fail(res, 409, 'conflict', 'only an app Bothy no longer uses can be dismissed');
  setApps(appsFor(req).filter((x) => x.id !== m[1]));
  json(res, 204);
});

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
  const app = appsFor(req).find((a) => a.connection_id === c.id);
  json(res, 200, { connection: c, repos: githubNoRepos(req) && !state.madeHere ? state.held.map((h) => ({ ...h, allowed: false })) : state.held, ...(app ? { app } : {}) });
});
on('POST', '/api/github/connections/{id}/test', (req, res) => json(res, 200, probe(req, connected(req)?.auth ?? 'pat', true)));
on('PUT', '/api/github/connections/{id}/repos', async (req, res) => {
  const want = (JSON.parse(await body(req)).repos as string[]).map((r) => r.split('/')[1]);
  for (const h of state.held) h.allowed = want.includes(h.name);
  for (const name of want) if (!state.held.some((h) => h.name === name)) state.held.push(held(name, true));
  json(res, 200, state.held);
});
on('DELETE', '/api/github/connections/{id}', (req, res) => {
  // Disconnecting retires the app Bothy made for the connection.
  const made = appsFor(req).some((a) => a.state === 'connected');
  if (made) setApps(appsFor(req).map((a) => (a.state === 'connected' ? madeApp('retired') : a)));
  state.github = null;
  state.held = [];
  state.madeHere = made || state.madeHere;
  json(res, 204);
});
on('GET', '/api/github/repos', (req, res) => json(res, 200, connected(req) && !(githubNoRepos(req) && !state.madeHere) ? state.held.filter((h) => h.allowed) : []));
// The repository's workflows (h_uistate.go githubWorkflows); the dialog falls back to a text field when GitHub can't list them.
on('GET', '/api/github/repos/{owner}/{repo}/workflows', (req, res) => {
  if (noWorkflows(req)) return fail(res, 502, 'unavailable', 'mock: GitHub did not list the workflows');
  const list: Workflow[] = [
    { id: 101, name: 'CI', path: '.github/workflows/ci.yml', state: 'active' },
    { id: 102, name: 'Deploy', path: '.github/workflows/deploy.yml', state: 'active' },
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

// ---- routes: web search check (h_uistate.go testSearch) ----

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
