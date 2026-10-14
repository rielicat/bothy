// Shell (C1): who is signed in, the event stream, banners and pausing, plus
// scenarios that fill the sidebar for review (a team's chat list, a long one).
import type { Approval, BannerData, Me, Message, Principal, Run, SearchHit, Session } from '../../src/lib/api/types.gen.ts';
import { approvals as approvalFixtures } from './approvals.ts';
import { active, ago, body, boot, clients, core, emit, fail, ids, isPaused, json, nowISO, on, onReset, owner, pausedBanner, pauseInfo, pausedNow, principals, routes, scenario, scenarios, viewer, ZERO, type Handler, type Req } from './core.ts';

const sealed = scenario('sealed', "Bothy's saved keys are locked (vault sealed banner).", 'shell');
const providerAuth = scenario('provider-auth', 'Anthropic stopped accepting the key (provider banner).', 'shell');
const safeMode = scenario('safe-mode', 'Bothy started with safe defaults after a settings error (banner).', 'shell');
const restart = scenario('restart', 'A changed setting needs a restart (banner).', 'shell');
const noBanners = scenario('no-banners', 'Drop the default banners.', 'shell');
const teamChats = scenario('team-chats', "A team's chat list: a group chat, chats from today, this week and earlier, one running, one waiting for approval, one untitled, one empty.", 'shell');
const longChats = scenario('long-chats', 'Forty-five chats, so the sidebar shows Show more (implies team-chats).', 'shell');
const idleChats = scenario('chats-idle', 'The team-chats list with nothing running and nothing waiting for approval (implies team-chats).', 'shell');

const team = (req?: Req) => teamChats(req) || longChats(req) || idleChats(req);

/** The banners this request should see: stored ones plus the scenario ones. */
export function banners(req?: Req): BannerData[] {
  const out = noBanners(req) ? [] : [...core.banners];
  if (pausedNow(req) && !out.some((b) => b.code === 'paused')) out.push(pausedBanner(req));
  if (sealed(req)) out.push({ level: 'error', code: 'vault_sealed', text: 'The vault is sealed.', action: '/settings/security/keys' });
  if (providerAuth(req)) out.push({ level: 'error', code: 'provider_auth', text: 'anthropic: 401 invalid x-api-key', action: '/settings/models' });
  if (safeMode(req)) out.push({ level: 'error', code: 'safe_mode', text: 'Started in safe mode: bothy.ui.yaml could not be loaded.', action: '/settings/advanced' });
  if (restart(req)) out.push({ level: 'info', code: 'restart_required', text: 'Restart to apply: sandbox.backend' });
  return out;
}

// The team scenarios: Maya Chen in #eng on Slack, with company work in her list.
const ENG = 'spc_01jteameng0000000000000000';
const eng: Principal = { id: ENG, kind: 'space', user_id: owner.id, display_name: '#eng', workspace_id: 'ws_01jteameng0000000000000000', tool_profile: 'restricted', created_at: ago(60 * 24 * 40) };
const T = {
  fix: 'ses_01jteamfix00000000000000000',
  launch: 'ses_01jteamlaunch000000000000000',
  draft: 'ses_01jteamdraft0000000000000000',
  support: 'ses_01jteamsupport00000000000000',
  eng: 'ses_01jteameng00000000000000000',
  pricing: 'ses_01jteampricing00000000000000',
  empty: 'ses_01jteamempty0000000000000000',
};

const chat = (id: string, title: string, minAgo: number, extra: Partial<Session> = {}): Session => ({
  id,
  principal_id: owner.id,
  origin: 'web',
  channel_key: '',
  title,
  model_slot: 'main',
  summary_upto: 0,
  epoch: 0,
  taint: 0,
  hidden: false,
  archived: false,
  created_at: ago(minAgo + 20),
  updated_at: ago(minAgo),
  last_message_at: ago(minAgo),
  ...extra,
});

const teamList = (): Session[] => [
  chat(T.eng, '#eng', 35, { principal_id: ENG, origin: 'slack', channel_key: 'slack:channel:C0ENG' }),
  chat(T.fix, 'Fix login redirect', 3),
  chat(T.launch, 'v2.3 launch checklist', 25),
  chat(T.draft, '', 50),
  chat(T.support, "Summarise yesterday's support tickets", 140),
  chat(T.pricing, 'Q3 pricing notes', 60 * 24 * 2),
  chat('ses_01jteamonboard00000000000000', 'Onboarding checklist for new engineers', 60 * 24 * 3),
  chat('ses_01jteamcache0000000000000000', 'Compare error rates before and after the cache change', 60 * 24 * 5),
  chat('ses_01jteamvendor000000000000000', 'Vendor contract renewal questions', 60 * 24 * 12),
  chat('ses_01jteampg0000000000000000000', 'Postgres upgrade plan', 60 * 24 * 30),
  // Created but never used: S14 keeps it out of the list.
  chat(T.empty, '', 1, { last_message_at: ZERO }),
];

const topics = ['Weekly metrics review', 'Customer call notes', 'Incident follow-up', 'Hiring loop feedback', 'Roadmap questions', 'Release notes draft', 'Billing export check', 'Security review notes', 'Design review summary'];
// Renames and deletions in the team lists, so the sidebar's menu can be tried out.
const edits = { titles: new Map<string, string>(), gone: new Set<string>() };
onReset(() => {
  edits.titles.clear();
  edits.gone.clear();
});
const edited = (list: Session[]) => list.filter((s) => !edits.gone.has(s.id)).map((s) => ({ ...s, title: edits.titles.get(s.id) ?? s.title }));

const longList = (): Session[] => [
  ...teamList(),
  ...Array.from({ length: 35 }, (_, i) => chat(`ses_01jteamlong${String(i).padStart(17, '0')}`, `${topics[i % topics.length]} ${Math.floor(i / topics.length) + 1}`, 60 * 24 * (6 + i * 2))),
];

const said: Record<string, string> = {
  [T.fix]: 'The login page sends people back to /home instead of the page they asked for. Find the cause and fix the redirect.',
  [T.launch]: "Here's a launch checklist for v2.3. Freeze main on Thursday, run the migration on staging, then announce in #eng.",
  [T.draft]: 'Draft the October release notes from the merged pull requests in acme/web and acme/api.',
  [T.support]: "Summarise yesterday's support tickets and group them by product area.",
  [T.pricing]: 'Summarise the Q3 pricing discussion and list the questions that are still open for the launch.',
};

const message = (sid: string, text: string): Message => ({ id: 1, session_id: sid, seq: 1, role: 'user', kind: 'normal', parts: [{ t: 'text', text }], provenance: 'user', tokens: 30, created_at: ago(50) });

/** Answers with h while `when` holds; otherwise the route another domain registered answers as usual. */
function overlay(method: string, path: string, when: (req: Req, m: RegExpMatchArray) => boolean, h: Handler) {
  const self: Handler = (req, res, m, q) => {
    if (when(req, m)) return h(req, res, m, q);
    const p = new URL(req.url ?? '/', 'http://mock').pathname;
    // Only routes after this one: another domain's wrapper in front of it falls through to here, so searching from the top would loop.
    const next = routes.slice(routes.findIndex((r) => r[2] === self) + 1).find((r) => r[0] === method && r[1].test(p));
    return next ? next[2](req, res, p.match(next[1])!, q) : fail(res, 404, 'not_found', `mock: no route for ${method} ${p}`);
  };
  on(method, path, self);
  // First in the table whatever order the domain files were loaded in (tests import fixtures.ts first).
  routes.unshift(routes.pop()!);
}

overlay('GET', '/api/sessions', team, (req, res) => json(res, 200, { items: edited(longChats(req) ? longList() : teamList()) }));
const ours = (req: Req, m: RegExpMatchArray) => team(req) && longList().some((s) => s.id === m[1]);
overlay('PATCH', '/api/sessions/{id}', ours, async (req, res, m) => {
  const { title } = JSON.parse(await body(req));
  edits.titles.set(m[1], title);
  json(res, 200, edited(longList()).find((s) => s.id === m[1]));
});
overlay('DELETE', '/api/sessions/{id}', ours, (_q, res, m) => {
  edits.gone.add(m[1]);
  json(res, 204);
});
overlay(
  'GET',
  '/api/sessions/{id}/messages',
  (req, m) => team(req) && m[1] in said,
  (_q, res, m) => json(res, 200, { messages: [message(m[1], said[m[1]])], has_more: false }),
);
overlay('GET', '/api/runs', (req) => team(req), (req, res, _m, q) => {
  const run = { id: 'run_01jteamfix00000000000000000', session_id: T.fix, principal_id: owner.id, user_id: owner.id, trigger: 'message', status: 'running', steps: 3, tool_calls: 2, usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 40_000, models: [], limits: { max_steps: 30, max_tokens: 0, max_time: 0, max_cost_micros: 0 }, created_at: ago(3), started_at: ago(3), finished_at: ZERO, refined: false } as Run;
  json(res, 200, { items: !idleChats(req) && (!q.get('status') || q.get('status') === 'running') ? [run] : [] });
});
overlay(
  'GET',
  '/api/approvals',
  (req, _m) => team(req) && ['pending', 'parked', null].includes(new URL(req.url ?? '/', 'http://mock').searchParams.get('status')),
  (req, res) => {
    const status = new URL(req.url ?? '/', 'http://mock').searchParams.get('status') ?? 'pending';
    const base = approvalFixtures[0];
    const items: Approval[] =
      status === 'pending' && !idleChats(req)
        ? [
            { ...base, id: 'apr_01jteamlaunch000000000000000', session_id: T.launch, created_at: ago(20) },
            { ...base, id: 'apr_01jteambrief0000000000000000', session_id: ids.brief, created_at: ago(40) },
          ]
        : [];
    json(res, 200, { items });
  },
);
// The team's inbox is those two requests and nothing to review, so the counts match the list.
overlay('GET', '/api/inbox', team, (req, res) => {
  const base = approvalFixtures[0];
  const asks = idleChats(req)
    ? []
    : [
        { ...base, id: 'apr_01jteamlaunch000000000000000', session_id: T.launch, created_at: ago(20) },
        { ...base, id: 'apr_01jteambrief0000000000000000', session_id: ids.brief, created_at: ago(40) },
      ];
  json(res, 200, { items: asks.map((a) => ({ id: a.id, type: 'approval', question: a.card.question ?? a.card.title, for_you: true, created_at: a.created_at, approval: a })) });
});
overlay('GET', '/api/search', team, (_q, res, _m, q) => {
  const term = (q.get('q') ?? '').toLowerCase();
  const hits: SearchHit[] = [];
  for (const [sid, text] of Object.entries(said)) {
    const i = text.toLowerCase().indexOf(term);
    if (term && i >= 0) hits.push({ session_id: sid, title: teamList().find((s) => s.id === sid)?.title ?? '', seq: 1, snippet: `${i > 30 ? '…' : ''}${text.slice(Math.max(0, i - 30), i)}[${text.slice(i, i + term.length)}]${text.slice(i + term.length, i + term.length + 50)}${i + term.length + 50 < text.length ? '…' : ''}`, at: ago(30), rank: 1, trusted: true });
  }
  json(res, 200, hits);
});

export function me(req?: Req): Me {
  const user = team(req) && viewer(req).role !== 'member' ? { ...viewer(req), display_name: 'Maya Chen', username: 'maya' } : viewer(req);
  const p = pauseInfo(req);
  return {
    user,
    csrf: 'mock-csrf',
    mode: core.mode,
    principals: team(req) ? [principals[0], eng] : principals,
    paused: isPaused(req),
    ...(p ? { paused_by: p.by.id, paused_by_name: p.by.display_name, paused_at: p.at } : {}),
    sandbox_available: true,
    banners: banners(req),
    access: { peer: 'local', tls: false },
  };
}

on('GET', '/api/auth/me', (req, res) => json(res, 200, me(req)));
on('POST', '/api/auth/logout', (_q, res) => {
  core.loggedIn = false;
  json(res, 204);
});

on('GET', '/api/events', (req, res) => {
  res.statusCode = 200;
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-store');
  res.write(`event: hello\ndata: ${JSON.stringify({ boot, seq: core.seq, paused: isPaused(req), sandbox: true, banners: banners(req) })}\n\n`);
  clients.add(res);
  const ping = setInterval(() => res.write(': ping\n\n'), 15000);
  req.on('close', () => {
    clearInterval(ping);
    clients.delete(res);
  });
});

on('POST', '/api/admin/pause', async (req, res) => {
  const was = core.paused;
  core.paused = JSON.parse(await body(req)).paused;
  if (core.paused && !was) Object.assign(core, { pausedBy: viewer(req), pausedAt: nowISO() });
  if (!core.paused) Object.assign(core, { pausedBy: null, pausedAt: '' });
  const b: BannerData = core.paused ? pausedBanner(req) : { level: 'error', code: 'paused', text: '', clear: true };
  core.banners = core.paused ? [...core.banners, b] : core.banners.filter((x) => x.code !== 'paused');
  emit('banner', b);
  json(res, 204);
});

// Not a gateway route: lists the scenario switches for people and the shot specs.
on('GET', '/api/mock/scenarios', (req, res) => json(res, 200, { active: Object.fromEntries(active(req)), scenarios: scenarios() }), { open: true });
