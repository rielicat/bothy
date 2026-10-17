// Approvals (C4): waiting and decided requests, multi-approver votes
// and the deploys approvals start (S17); inbox.ts lists the other inbox items (S12).
import type { Action, Approval, ApprovalVotes, Card, Choice, SideEffect } from '../../src/lib/api/types.gen.ts';
import type { Deploy } from '../../src/components/approval/types.ts';
import { body, clone, core, emit, fail, ids, json, member, nowISO, on, onReset, owner, scenario, sleep, viewer, type Req } from './core.ts';

const none = scenario('approvals-empty', 'Nothing waits for approval and nothing was decided.', 'approvals');
const all = scenario('approvals-all', 'Adds one waiting card for every kind of action (docs/design/ui.md §8.2).', 'approvals');
const hold = scenario('approvals-hold', 'Decisions are never answered, to show the sending state.', 'approvals');
const decideFails = scenario('approvals-fail', 'Decisions fail, to show "Couldn\'t send your decision".', 'approvals');
const voted = scenario('deploy-voted', 'You already approved the production deploy; it waits for one more.', 'approvals');
const requester = scenario('deploy-requester', 'You asked for the production deploy, so others must approve it.', 'approvals');
const ineligible = scenario('deploy-ineligible', 'You are not one of the production deploy approvers.', 'approvals');
const listDown = scenario('approvals-down', "Listing approvals fails, to show the page that didn't load.", 'approvals');

const maya = 'usr_01jmaya000000000000000000';
const priya = 'usr_01jpriya00000000000000000';
const names: Record<string, string> = { [owner.id]: 'Sam', [member.id]: 'Alex', [maya]: 'Maya Chen', [priya]: 'Priya Rao' };

// Times are taken at each reset, so fixtures never expire while a preview server runs.
let t0 = Date.now();
const ago = (min: number) => new Date(t0 - min * 60_000).toISOString();
const ahead = (min: number) => new Date(t0 + min * 60_000).toISOString();

const action = (tool: string, kind: string, se: SideEffect, payload: Record<string, unknown>, extra: Partial<Action> = {}): Action => ({ tool, kind, side_effect: se, payload, fingerprint: `fp_${tool}`, ...extra });

const choicesFor = (se: SideEffect, tool: string): Choice[] =>
  tool === 'release_deploy' || se === 'financial' || se === 'admin' ? ['allow_once', 'deny'] : se === 'irreversible' ? ['allow_once', 'allow_session', 'deny'] : ['allow_once', 'allow_session', 'allow_always', 'deny'];

interface Spec {
  id: string;
  title: string;
  act: Action;
  context?: string;
  session?: string;
  warnings?: string[];
  /** Card fields the server adds beside Tool, such as a named chat's To. */
  fields?: Card['fields'];
  choices?: Choice[];
  created: number;
  expires?: number;
  status?: string;
  extra?: Partial<Approval>;
}

// The v0.1 card the gateway renders today; the UI builds its words from the Action (lib/cardcopy.ts).
function make(s: Spec): Approval {
  const choices = s.choices ?? choicesFor(s.act.side_effect, s.act.tool);
  const card: Card = {
    title: s.title,
    risk: s.act.side_effect,
    fields: [{ label: 'Tool', value: s.act.tool, mono: true }, ...(s.fields ?? [])],
    diff: s.act.diff,
    warnings: s.warnings,
    context: s.context ?? 'Web · session “Clean up staging notes”',
    choices,
    expires: ahead(s.expires ?? 15),
    web_url: `https://bothy.example/approvals/${s.id}`,
  };
  return {
    id: s.id,
    run_id: `run_${s.id.slice(4)}`,
    // Only the trip chat's own request belongs to a chat fixture; the chat page lists its waiting cards.
    session_id: s.session ?? 'ses_01jcleanup000000000000000',
    principal_id: owner.id,
    tool_call_id: `tcl_${s.id.slice(4)}`,
    action: s.act,
    decision: { verdict: 'ask', rule: `default:${s.act.side_effect}`, reason: 'Changes things outside this chat', floor: false, approver: { user_id: owner.id, admin_only: false }, timeout: 15 * 60e9, on_timeout: 'deny', choices },
    card,
    status: s.status ?? 'pending',
    approver: { user_id: owner.id, admin_only: false },
    on_timeout: 'deny',
    created_at: ago(s.created),
    expires_at: ahead(s.expires ?? 15),
    ...s.extra,
  };
}

const deleteDiff = '--- a/notes/staging.md\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-# Staging\n-Deploy from the release branch only.\n-Ask Maya before touching the database.\n';
const prDiff = [
  'Repository: acme/web (private)',
  'Branch: bothy/fix-login-redirect → main',
  'Title: Fix login redirect losing the return address',
  '',
  'diff --git a/src/auth/redirect.ts b/src/auth/redirect.ts',
  '--- a/src/auth/redirect.ts',
  '+++ b/src/auth/redirect.ts',
  '@@ -10,7 +10,9 @@ export function redirectAfterLogin(req: Request) {',
  '   const next = req.query.next;',
  '-  if (!next) return "/";',
  '-  return next;',
  '+  if (!next || !next.startsWith("/")) return "/";',
  '+  // Keep the address the person was going to.',
  '+  return encodeURI(next);',
  ' }',
  'diff --git a/src/auth/redirect.test.ts b/src/auth/redirect.test.ts',
  'new file mode 100644',
  '--- /dev/null',
  '+++ b/src/auth/redirect.test.ts',
  '@@ -0,0 +1,4 @@',
  '+import { redirectAfterLogin } from "./redirect";',
  '+test("keeps the return address", () => {',
  '+  expect(redirectAfterLogin(req("/settings"))).toBe("/settings");',
  '+});',
  'diff --git a/CHANGELOG.md b/CHANGELOG.md',
  '--- a/CHANGELOG.md',
  '+++ b/CHANGELOG.md',
  '@@ -1,3 +1,4 @@',
  ' # Changelog',
  '+- Signing in returns you to the page you asked for.',
  ' ',
  '-## 2.3.0',
  '+## 2.3.1',
].join('\n');

const deployAction = (repo: string, env = 'production', ref = 'main') =>
  action('release_deploy', 'release', 'irreversible', { environment: env, repo, workflow: 'deploy.yml', ref, inputs: {} }, { recipient: `github:${repo}` });

// Deploys wait for two approvals; whoever asked never counts.
function deploy(id: string, repo: string, opts: { requester: string; created: number; votes?: { user: string; choice: Choice; at: number; comment?: string }[]; status?: string; context?: string }): Approval {
  return make({
    id,
    title: 'Deploy a release',
    act: deployAction(repo),
    context: opts.context ?? 'Slack · group · session “Release train”',
    created: opts.created,
    expires: 120,
    status: opts.status,
    extra: {
      principal_id: opts.requester,
      chat: { channel: 'slack', chat_id: 'C01ENG', kind: 'group' },
      approver: { user_id: '', admin_only: false, users: [owner.id, priya, maya], roles: ['admin'], required: 2, requester: opts.requester },
      votes: (opts.votes ?? []).map((v) => ({ approval_id: id, user_id: v.user, choice: v.choice, via: 'web', at: ago(v.at), comment: v.comment })),
    },
  });
}

// Requests from people and channels in chat apps (approver.team): an admin
// here, or a chat approver in their Slack messages, decides them.
const ENG = 'spc_01jeng00000000000000000000';
const ALEX = 'spc_01jalexslack000000000000000';
const alexAsks = { user: '', name: 'Alex Kim', app: 'slack', identity: 'slack:U02ALEX', handle: 'alexk', kind: 'teammate' };
const teamApprover = { user_id: '', admin_only: false, team: true, requester_identity: 'slack:U02ALEX' };

function team(s: Spec & { principal: string; chat: string }): Approval {
  const a = make({ ...s, context: s.chat ? `${s.chat} on Slack` : "Alex Kim's direct messages on Slack", session: s.session ?? `ses_01jteam${s.id.slice(8, 20)}`.padEnd(30, '0') });
  return {
    ...a,
    principal_id: s.principal,
    chat: s.chat ? { channel: 'slack', chat_id: 'C01ENG', kind: 'group' } : { channel: 'slack', chat_id: 'D02ALEX', kind: 'dm' },
    approver: teamApprover,
    decision: { ...a.decision, approver: teamApprover },
    requester: s.chat ? { ...alexAsks, chat: s.chat, kind: 'channel' } : alexAsks,
    ...s.extra,
  };
}

function teamList(): Approval[] {
  return [
    // A channel's request offers no standing permissions.
    team({
      id: 'apr_01jteamchan000000000000000',
      principal: ENG,
      chat: '#eng',
      title: 'Write file',
      act: action('fs_write', 'fs.write', 'local_write', { path: 'notes/standup.md', content: '- Alex: login redirect fix\n- Priya: pricing page', mode: 'create' }, { paths: ['notes/standup.md'] }),
      choices: ['allow_once', 'deny'],
      // The newest requests, so the inbox keeps its order for the account's.
      created: 0.6,
      expires: 55,
    }),
    team({
      id: 'apr_01jteamdm00000000000000000',
      principal: ALEX,
      chat: '',
      title: 'Run shell command',
      act: action('shell', 'exec', 'local_write', { argv: ['python3', 'sales_report.py', '--week', '41'], cwd: '' }, { argv: ['python3', 'sales_report.py', '--week', '41'] }),
      created: 0.4,
      expires: 58,
    }),
  ];
}

// The trip chat's request stays first: chat tests take it as fixtures.approvals[0].
function waitingList(): Approval[] {
  return [
    make({
      id: 'apr_01jshell00000000000000000',
      title: 'Run shell command',
      act: action('shell', 'exec', 'local_write', { argv: ['bash', '-lc', "python3 build_agenda.py --out plans/offsite-agenda.html --city 'Edinburgh'"], cwd: 'plans/' }, {
        argv: ['bash', '-lc', "python3 build_agenda.py --out plans/offsite-agenda.html --city 'Edinburgh'"],
        cwd: 'plans/',
      }),
      context: 'Web · session “Edinburgh team offsite”',
      session: ids.trip,
      created: 30,
      expires: 10,
    }),
    deploy('apr_01jdeployweb0000000000000', 'acme/web', { requester: member.id, created: 40, votes: [{ user: maya, choice: 'allow_once', at: 32 }] }),
    make({
      id: 'apr_01jdelete0000000000000000',
      title: 'Delete file',
      act: action('fs_delete', 'fs.delete', 'irreversible', { path: 'notes/staging.md' }, { paths: ['notes/staging.md'], diff: deleteDiff }),
      created: 2,
      expires: 45,
    }),
    make({
      id: 'apr_01jpr00000000000000000000',
      title: 'Open pull request',
      act: action(
        'repo_open_pr',
        'send',
        'external_send',
        { repo: 'acme/web', base: 'main', head: 'bothy/fix-login-redirect', title: 'Fix login redirect losing the return address', body: '', draft: false, files: [] },
        { recipient: 'github:acme/web', diff: prDiff },
      ),
      context: 'Web · session “Login bug”',
      warnings: ['First contact with github:acme/web.'],
      choices: ['allow_once', 'deny'],
      created: 1,
      expires: 50,
    }),
    make({
      id: 'apr_01jfetch00000000000000000',
      title: 'Send a message',
      act: action('message', 'send', 'external_send', { text: 'Morning. Three pull requests wait for review in acme/web, and the staging deploy failed at 06:10.' }, { recipient: 'telegram:-100223' }),
      context: 'Telegram · DM · schedule · session “Morning briefing”',
      session: ids.brief,
      // The server names a group chat Bothy was added to, keeping its id beside the title.
      fields: [{ label: 'To', value: '“Ops team” on Telegram (chat -100223)', mono: false }],
      warnings: ['First contact with telegram:-100223.'],
      choices: ['allow_once', 'deny'],
      created: 25,
      status: 'parked',
      extra: { on_timeout: 'park', park_until: ahead(60 * 22), chat: { channel: 'telegram', chat_id: '5512', kind: 'dm' } },
    }),
    deploy('apr_01jdeployapi0000000000000', 'acme/api', { requester: owner.id, created: 20, votes: [{ user: priya, choice: 'allow_once', at: 12 }], context: 'Web · session “API release”' }),
    ...teamList(),
  ];
}

// One waiting card per row of docs/design/ui.md §8.2, for review shots.
function everyKind(): Approval[] {
  const k = (id: string, title: string, act: Action, extra: Partial<Spec> = {}) => make({ id: `apr_01jrow${id}`.padEnd(30, '0'), title, act, created: 5, expires: 50, session: 'ses_01jrows000000000000000000', ...extra });
  return [
    k('create', 'Write file', action('fs_write', 'fs.write', 'local_write', { path: 'notes/launch-checklist.md', content: '# v2.3 launch\n- Freeze main on Monday\n- Tag the release\n- Post in #eng', mode: 'create' }, { paths: ['notes/launch-checklist.md'], diff: '--- /dev/null\n+++ b/notes/launch-checklist.md\n@@ -0,0 +1,4 @@\n+# v2.3 launch\n+- Freeze main on Monday\n+- Tag the release\n+- Post in #eng\n' })),
    k('change', 'Write file', action('fs_write', 'fs.write', 'local_write', { path: 'notes/launch-checklist.md', content: '…', mode: 'overwrite' }, { paths: ['notes/launch-checklist.md'], diff: '--- a/notes/launch-checklist.md\n+++ b/notes/launch-checklist.md\n@@ -1,4 +1,4 @@\n # v2.3 launch\n-- Freeze main on Monday\n+- Freeze main on Tuesday\n - Tag the release\n - Post in #eng\n' })),
    k('append', 'Write file', action('fs_write', 'fs.write', 'local_write', { path: 'notes/standup.md', content: '- Sam: pricing page copy\n- Alex: login redirect fix', mode: 'append' }, { paths: ['notes/standup.md'] })),
    k('python', 'Run Python code', action('python', 'exec', 'local_write', { argv: ['python3', '.bothy/scripts/5f2c.py'], code: 'import csv\nrows = list(csv.DictReader(open("sales.csv")))\nprint(sum(float(r["total"]) for r in rows))', cwd: '' })),
    k('post', 'Fetch a web page', action('web_fetch', 'http', 'external_send', { url: 'https://api.example.com/v1/reports', method: 'POST', body: { week: 41 } }, { url: 'https://api.example.com/v1/reports', host: 'api.example.com', port: 443, method: 'POST' }), { warnings: ['First contact with api.example.com.'] }),
    k('memory', 'Update memory', action('memory', 'memory', 'local_write', { file: 'MEMORY', op: 'add', new: 'Releases go out on Tuesdays after the 10:00 stand-up.' }), { warnings: ['Started by a message from an untrusted sender.'] }),
    k('notes', 'Project notes', action('notes', 'memory', 'local_write', { repo: 'acme/web', op: 'replace' }, { diff: '--- a/notes\n+++ b/notes\n@@ -1,2 +1,2 @@\n Deploys run from the release branch.\n-Ask Maya before migrations.\n+Ask Maya or Priya before migrations.\n' }), { warnings: ['Started by a message from an untrusted sender.'] }),
    k('schedcreate', 'Create schedule', action('schedule_create', 'schedule', 'admin', { name: 'Monday pull requests', kind: 'cron', spec: '0 9 * * 1', tz: 'Europe/London', when: 'Every Monday at 9:00', prompt: 'List the open pull requests in acme/web.\nGroup them by author.\nFlag any older than a week.\nEnd with one line on what to review first.', deliver: { channel: 'slack', chat_id: 'C01ENG', kind: 'group' } })),
    k('scheddelete', 'Delete schedule', action('schedule_delete', 'schedule', 'admin', { id: 'sch_01jbrief00000000000000000', name: 'Morning briefing' })),
    k('netallow', 'Allow a website', action('net_allow', 'net', 'external_send', { host: 'registry.npmjs.org' }, { host: 'registry.npmjs.org', port: 443 }), { choices: ['allow_session', 'deny'] }),
    k('comment', 'Comment on GitHub', action('github_comment', 'send', 'external_send', { repo: 'acme/web', number: 482, body: 'The redirect fix is ready for review. It keeps the return address and adds a test.' }, { recipient: 'github:acme/web' }), {
      warnings: ['This session has seen private data and untrusted content; anything sent out could leak it.'],
    }),
    k('mcp', 'Create issue', action('mcp__linear__create_issue', 'mcp', 'external_send', { title: 'Login redirect drops the return address', team: 'WEB', priority: 2 }, { mcp_server: 'linear', mcp_tool: 'create_issue' })),
    k('mcpirrev', 'Delete project', action('mcp__linear__delete_project', 'mcp', 'irreversible', { project: 'Q3 pricing' }, { mcp_server: 'linear', mcp_tool: 'delete_project' })),
    k('admin', 'Change settings', action('config_set', 'generic', 'admin', { setting: 'Daily spending limit', value: '$20.00' }), { extra: { approver: { user_id: '', admin_only: true } } }),
    k('pay', 'Pay invoice', action('mcp__stripe__pay_invoice', 'generic', 'financial', { amount: '$240.00', to: 'Northwind Hosting' }, { recipient: 'Northwind Hosting' })),
    k('unknown', 'Install skill', action('skill_install', 'generic', 'local_write', { name: 'release-notes', source: 'https://skills.example.com/release-notes.zip' })),
    k('deploy1', 'Deploy a release', deployAction('acme/docs', 'staging', 'v2.3.1')),
  ];
}

const decided = (a: Approval, status: string, choice: Choice | undefined, by: string, via: string, at: number, extra: Partial<Approval> = {}): Approval => ({ ...a, status, choice, decided_by: by, decided_via: via, decided_at: ago(at), created_at: ago(at + 1), ...extra });

function recentList(): Approval[] {
  const fsDel = (id: string, path: string) => make({ id, title: 'Delete file', act: action('fs_delete', 'fs.delete', 'irreversible', { path }, { paths: [path] }), created: 0 });
  const write = make({ id: 'apr_01jdonewrite0000000000000', title: 'Write file', act: action('fs_write', 'fs.write', 'local_write', { path: 'notes/q3-pricing.md', content: '…', mode: 'overwrite' }, { paths: ['notes/q3-pricing.md'] }), created: 0 });
  const msg = make({ id: 'apr_01jdonemsg000000000000000', title: 'Send a message', act: action('message', 'send', 'external_send', { text: 'Stand-up starts in five minutes.' }, { recipient: 'slack:C01ENG' }), created: 0, context: 'Slack · group · session “Stand-up”' });
  const sh = make({ id: 'apr_01jdoneshell0000000000000', title: 'Run shell command', act: action('shell', 'exec', 'local_write', { argv: ['bash', '-lc', 'rm -rf build/'] }, { argv: ['bash', '-lc', 'rm -rf build/'] }), created: 0 });
  const fetchPost = make({ id: 'apr_01jdonefetch0000000000000', title: 'Fetch a web page', act: action('web_fetch', 'http', 'external_send', { url: 'https://api.example.com/v1/reports' }, { url: 'https://api.example.com/v1/reports', host: 'api.example.com', method: 'POST' }), created: 0 });
  const day = 60 * 24;
  return [
    decided(fsDel('apr_01jdonedel10000000000000', 'notes/old-draft.md'), 'allowed', 'allow_once', owner.id, 'web', 12),
    decided(write, 'allowed', 'allow_session', owner.id, 'web', 25),
    decided(msg, 'allowed', 'allow_always', owner.id, 'web', 40),
    decided(sh, 'denied', 'deny', owner.id, 'web', 55, { comment: 'Not while the build server is in use' }),
    decided(deploy('apr_01jdonedeploy100000000000', 'acme/web', { requester: member.id, created: 0, votes: [{ user: maya, choice: 'allow_once', at: 70 }, { user: priya, choice: 'allow_once', at: 66 }] }), 'allowed', 'allow_once', priya, 'web', 66),
    decided(deploy('apr_01jdonedeploy200000000000', 'acme/api', { requester: owner.id, created: 0, votes: [{ user: maya, choice: 'allow_once', at: 90 }, { user: member.id, choice: 'allow_once', at: 88 }] }), 'allowed', 'allow_once', member.id, 'web', 88),
    decided(fsDel('apr_01jdonedel20000000000000', 'reports/q2-draft.pdf'), 'allowed', 'allow_once', member.id, 'slack', day + 30),
    decided(fsDel('apr_01jdonedel30000000000000', 'reports/q1-draft.pdf'), 'denied', 'deny', member.id, 'telegram', day + 50),
    decided(deploy('apr_01jdonedeploy300000000000', 'acme/web', { requester: member.id, created: 0, votes: [{ user: maya, choice: 'allow_once', at: day + 80 }, { user: priya, choice: 'allow_once', at: day + 75 }] }), 'allowed', 'allow_once', priya, 'web', day + 75),
    decided(deploy('apr_01jdonedeploy400000000000', 'acme/web', { requester: member.id, created: 0, votes: [{ user: maya, choice: 'deny', at: 3 * day, comment: 'Not before the freeze ends' }] }), 'denied', 'deny', maya, 'web', 3 * day),
    decided(fetchPost, 'expired', undefined, '', 'timeout', 4 * day),
    decided(sh, 'cancelled', undefined, '', 'stop', 5 * day, { id: 'apr_01jdonestop00000000000000' }),
    decided(deploy('apr_01jdonedeploy500000000000', 'acme/api', { requester: owner.id, created: 0, votes: [{ user: owner.id, choice: 'deny', at: 6 * day }] }), 'denied', 'deny', owner.id, 'web', 6 * day),
    // Sam approved Alex Kim's request from the card in his Slack messages.
    decided(teamList()[1], 'allowed', 'allow_once', '', 'slack', 35, { id: 'apr_01jteamdone0000000000000', decided_by_identity: 'slack:U01SAM', decided_by_name: 'Sam · Slack' }),
  ];
}

// What became of the approved deploys (§8.7): one running, one done, one failed.
function deployList(): Deploy[] {
  const d = (approval: string, repo: string, status: string, startedAgo: number, extra: Partial<Deploy> = {}): Deploy => ({
    id: `dep_${approval.slice(4)}`,
    environment: 'production',
    repo,
    ref: 'main',
    approval_id: approval,
    status,
    run_url: `https://github.com/${repo}/actions/runs/1029384756`,
    created_at: ago(startedAgo),
    ...extra,
  });
  return [
    d('apr_01jdonedeploy100000000000', 'acme/web', 'running', 65),
    d('apr_01jdonedeploy200000000000', 'acme/api', 'succeeded', 87, { finished_at: ago(84) }),
    d('apr_01jdonedeploy300000000000', 'acme/web', 'failed', 60 * 24 + 74, { finished_at: ago(60 * 24 + 71), failed_step: 'Run migrations', error: 'exit status 1' }),
  ];
}

export const approvals: Approval[] = waitingList();
export const recentApprovals: Approval[] = recentList();

export const state = { approvals: [] as Approval[], recent: [] as Approval[], deploys: [] as Deploy[] };
onReset(() => {
  t0 = Date.now();
  Object.assign(state, { approvals: [...waitingList(), ...everyKind()], recent: recentList(), deploys: deployList() });
});

// Scenarios ride along on every request (?mock= sets a cookie), so the every-kind cards are stored
// always and listed only when asked for.
const everyKindCard = (a: Approval) => a.id.startsWith('apr_01jrow');

/** Runs waiting on a decision, by approval id; chat.ts parks its scripted run here. */
export const waiting = new Map<string, (choice: string) => void>();

const rank: Record<string, number> = { member: 1, admin: 2, owner: 3 };

/** The deploy fixture as one viewer state or another. */
function asSeen(a: Approval, req?: Req): Approval {
  if (a.id !== 'apr_01jdeployweb0000000000000' || a.status !== 'pending') return a;
  const me = viewer(req).id;
  const out = clone(a);
  if (voted(req)) out.votes = [{ approval_id: a.id, user_id: me, choice: 'allow_once', via: 'web', at: ago(5) }];
  if (requester(req)) out.approver.requester = out.principal_id = me;
  if (ineligible(req)) Object.assign(out.approver, { users: [maya, priya], roles: [] });
  return out;
}

// Team requests reach admins only, as approvalVisible says.
const seesTeam = (a: Approval, req: Req) => !a.approver.team || (rank[viewer(req).role] ?? 0) >= rank.admin;

export function list(req: Req): Approval[] {
  return state.approvals.filter((a) => (all(req) || !everyKindCard(a)) && seesTeam(a, req)).map((a) => asSeen(a, req));
}

function votesOf(a: Approval, req: Req): ApprovalVotes {
  const me = viewer(req);
  const r = a.approver;
  const vs = (a.votes ?? []).map((v) => ({ user_id: v.user_id, name: names[v.user_id] ?? 'Someone', choice: v.choice, via: v.via, at: v.at, comment: v.comment }));
  const mine = vs.find((v) => v.user_id === me.id)?.choice;
  const open = a.status === 'pending' || a.status === 'parked';
  const eligible = !!r.users?.includes(me.id) || !!r.roles?.some((ro) => (rank[me.role] ?? 0) >= (rank[ro] ?? 9));
  // As the server words it: who can still approve, the viewer first as "you".
  const still = (r.users ?? []).filter((u) => u !== r.requester && !vs.some((v) => v.user_id === u));
  const people = [...still.filter((u) => u === me.id).map(() => 'you'), ...still.filter((u) => u !== me.id).map((u) => names[u] ?? 'Someone')];
  if (r.roles?.includes('admin')) people.push('any admin');
  const approvers = people.length > 1 ? `${people.slice(0, -1).join(', ')} or ${people[people.length - 1]}` : (people[0] ?? '');
  return {
    approval_id: a.id,
    status: a.status,
    required: r.required ?? 1,
    approvals: vs.filter((v) => v.choice === 'allow_once' && v.user_id !== r.requester).length,
    votes: vs,
    approvers,
    requester: r.requester ?? '',
    requester_name: names[r.requester ?? ''] ?? '',
    can_vote: open && !mine && me.id !== r.requester && eligible,
    can_withdraw: open && !mine && me.id === r.requester,
    your_vote: mine,
  };
}

const multi = (a: Approval) => !a.approver.user_id && !a.approver.admin_only && !!(a.approver.users?.length || a.approver.roles?.length);

/** Whether the viewer can decide it now: the inbox's for_you (S12). */
export function forYou(a: Approval, req: Req): boolean {
  const me = viewer(req);
  if (multi(a)) return votesOf(a, req).can_vote;
  if (a.approver.admin_only || a.approver.team) return (rank[me.role] ?? 0) >= rank.admin;
  return a.approver.user_id === me.id;
}

on('GET', '/api/approvals', (req, res, _m, q) => {
  if (listDown(req)) return fail(res, 503, 'unavailable', 'mock: approvals-down');
  if (none(req)) return json(res, 200, { items: [] });
  const status = q.get('status') ?? 'pending';
  json(res, 200, { items: status === 'recent' ? state.recent.filter((a) => seesTeam(a, req)) : list(req).filter((a) => a.status === status) });
});

on('GET', '/api/approvals/{id}', (req, res, m) => {
  const a = [...list(req), ...state.recent].find((x) => x.id === m[1]);
  return a ? json(res, 200, a) : fail(res, 404, 'not_found', 'No such approval.');
});

on('GET', '/api/approvals/{id}/votes', (req, res, m) => {
  const a = [...list(req), ...state.recent].find((x) => x.id === m[1]);
  if (!a) return fail(res, 404, 'not_found', 'No such approval.');
  if (!multi(a)) return fail(res, 400, 'invalid', 'this approval is decided by one approver, not by votes');
  json(res, 200, votesOf(a, req));
});

on('POST', '/api/approvals/{id}', async (req, res, m) => {
  const stored = state.approvals.find((x) => x.id === m[1]);
  if (!stored) return fail(res, 404, 'not_found', 'No such approval.');
  if (stored.status !== 'pending' && stored.status !== 'parked') return fail(res, 409, 'conflict', 'Already decided.');
  const { choice, comment } = JSON.parse(await body(req)) as { choice: Choice; comment?: string };
  if (hold(req)) await sleep(60_000);
  if (decideFails(req)) return fail(res, 500, 'internal', 'mock: approvals-fail');
  const a = asSeen(stored, req);
  const me = viewer(req).id;
  if (multi(a)) {
    a.votes = [...(a.votes ?? []), { approval_id: a.id, user_id: me, choice, via: 'web', at: nowISO(), comment }];
    const yes = a.votes.filter((v) => v.choice === 'allow_once' && v.user_id !== a.approver.requester).length;
    if (choice === 'deny') Object.assign(a, { status: 'denied', choice, decided_by: me, decided_via: 'web', decided_at: nowISO(), comment });
    else if (yes >= (a.approver.required ?? 1)) Object.assign(a, { status: 'allowed', choice, decided_by: me, decided_via: 'web', decided_at: nowISO() });
  } else {
    Object.assign(a, { status: choice === 'deny' ? 'denied' : 'allowed', choice, decided_by: core.me.id, decided_via: 'web', decided_at: nowISO(), comment });
  }
  Object.assign(stored, a);
  if (a.status === 'pending') return json(res, 200, a);
  state.recent.unshift(stored);
  emit('approval.resolved', { approval_id: a.id, status: a.status, choice, by: me, via: 'web' }, a.session_id, a.run_id);
  waiting.get(a.id)?.(choice);
  waiting.delete(a.id);
  json(res, 200, a);
});
