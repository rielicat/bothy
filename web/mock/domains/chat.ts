// Chat (C3): sessions, messages, pins, uploads, search, the run records of
// chat runs, and the scripted run that streams a reply (with an approval card
// when a message asks to run, send or delete something).
import type { Approval, Message, RunDetail, Session, ToolCallRecord } from '../../src/lib/api/types.gen.ts';
import { state as approvalState, waiting } from './approvals.ts';
import { ago, ahead, body, clone, core, emit, fail, ids, isPaused, json, member, newId, nowISO, on, onReset, owner, routes, scenario, sleep, type Req } from './core.ts';

const ses = (id: string, title: string, origin: string, minAgo: number, extra: Partial<Session> = {}): Session => ({
  id,
  principal_id: owner.id,
  origin,
  channel_key: origin === 'web' || origin === 'schedule' ? '' : `${origin}:dm:123`,
  title,
  model_slot: 'main',
  summary_upto: 0,
  epoch: 0,
  taint: 0,
  hidden: false,
  archived: false,
  created_at: ago(minAgo + 30),
  updated_at: ago(minAgo),
  last_message_at: ago(minAgo),
  ...extra,
});

// Chat-only ids carry a marker so this domain answers /api/runs/{id} for its
// own runs and leaves every other run to the activity domain.
const pad = (p: string, name: string) => `${p}_0chat${name}`.padEnd(p.length + 27, '0');
const sid = (name: string) => pad('ses', name);
const rid = (name: string) => pad('run', name);
const tcl = (name: string) => pad('tcl', name);
const CHAT_RUN = /^\/api\/runs\/(run_0chat[0-9a-z]+)$/;

/** Today at h:m, or that time `days` back. */
const at = (h: number, m: number, days = 0) => {
  const d = new Date();
  d.setDate(d.getDate() - days);
  d.setHours(h, m, 0, 0);
  return d.toISOString();
};

const S = { staging: sid('staging'), login: sid('login'), pricing: sid('pricing'), notices: sid('notices'), decided: sid('decided'), paused: sid('paused') };
const R = { staging: rid('staging'), login1: rid('login1'), login2: rid('login2'), pricing: rid('pricing'), notices: rid('notices'), decided: rid('decided') };

export const sessions: Session[] = [
  ses(S.staging, 'Clean up staging notes', 'web', 3),
  ses(ids.trip, 'Weekend in Edinburgh', 'web', 12, { taint: 2 | 4 }),
  ses(S.login, 'Fix login redirect', 'web', 40),
  ses('ses_01jinbox00000000000000000', 'Summarize the newsletter', 'telegram', 95, { taint: 1 | 2 | 4 }),
  ses(S.notices, 'Weekly status summary', 'web', 120, { taint: 1 | 2 | 4 }),
  ses(S.decided, 'Tidy the shared drive', 'web', 150),
  ses(S.paused, 'Plan the team offsite', 'telegram', 160),
  ses(ids.brief, 'Morning briefing · Mon 12 Oct', 'schedule', 60 * 9),
  ses(S.pricing, 'Q3 pricing notes', 'web', 60 * 22, { created_at: at(15, 2, 1), last_message_at: at(15, 10, 1) }),
  ses('ses_01jbudget0000000000000000', 'Household budget spreadsheet', 'web', 60 * 26, { taint: 1 }),
  ses('ses_01jslack00000000000000000', 'Release notes draft', 'slack', 60 * 50),
  ses('ses_01jrecipe0000000000000000', 'Dinner ideas without dairy', 'discord', 60 * 24 * 9),
  ses('ses_01jfamily0000000000000000', 'Family group', 'telegram', 40, { principal_id: 'spc_01jfamily0000000000000000', taint: 2 }),
];

let msgId = 1000;
const msg = (session: string, seq: number, role: string, parts: Message['parts'], extra: Partial<Message> = {}): Message => ({
  id: ++msgId,
  session_id: session,
  seq,
  role,
  kind: 'normal',
  parts,
  provenance: role === 'user' ? 'user' : role === 'tool' ? 'tool' : 'system',
  sender: role === 'user' ? `web:${owner.id}` : undefined,
  tokens: 40,
  created_at: ago(30 - seq),
  ...extra,
});
const text = (t: string) => ({ t: 'text', text: t });
const call = (id: string, name: string, args: unknown) => ({ t: 'tool_call', call: { id, name, args } });
const result = (call_id: string, name: string, content: string, extra: { is_error?: boolean; untrusted?: boolean } = {}) => ({ t: 'tool_result', result: { call_id, name, content, ...extra } });

const T = ids.trip;
const RUN1 = ids.tripRun;
const callbackDiff = [
  '--- a/src/auth/callback.ts',
  '+++ b/src/auth/callback.ts',
  '@@ -10,7 +10,9 @@ export async function callback(req: Request) {',
  '   const session = await finishSignIn(req);',
  '-  return redirect("/");',
  '+  const next = new URL(req.url).searchParams.get("next") ?? "/";',
  '+  // Only addresses on this site, so a link cannot send people elsewhere.',
  '+  return redirect(next.startsWith("/") && !next.startsWith("//") ? next : "/");',
  ' }',
].join('\n');

export const messages: Record<string, Message[]> = {
  [S.staging]: [
    msg(S.staging, 1, 'user', [text('The staging notes are out of date. Delete them.')], { created_at: ago(4) }),
    msg(S.staging, 2, 'assistant', [text("I'll check what's in the folder first."), call('s1', 'fs_list', { path: 'notes' }), call('s2', 'fs_read', { path: 'notes/staging.md' })], { run_id: R.staging, created_at: ago(4) }),
    msg(S.staging, 3, 'tool', [result('s1', 'fs_list', 'staging.md\nrelease.md\nonboarding.md'), result('s2', 'fs_read', '# Staging\nThe old staging cluster on k8s-01. Deploy with make stage.')], { run_id: R.staging, created_at: ago(4) }),
    msg(S.staging, 4, 'assistant', [text('It only describes the old staging cluster, which was shut down in August. I’ll delete it.'), call('s3', 'fs_delete', { path: 'notes/staging.md' })], { run_id: R.staging, created_at: ago(3) }),
  ],
  [T]: [
    msg(T, 1, 'user', [text('Plan a relaxed weekend in Edinburgh for two. We like food, walking and old bookshops. Budget around £400.')]),
    msg(T, 2, 'assistant', [text("I'll check the weather and a few current recommendations first."), call('call_1', 'web_search', { query: 'Edinburgh weekend weather forecast' }), call('call_2', 'web_fetch', { url: 'https://www.edinburghbookshops.example/guide' })], { run_id: RUN1 }),
    msg(
      T,
      3,
      'tool',
      [
        result('call_1', 'web_search', 'Sat: 14°C, light showers in the morning. Sun: 16°C, sunny spells.', { untrusted: true }),
        result('call_2', 'web_fetch', 'A guide to independent bookshops in the Old Town and Stockbridge…', { untrusted: true }),
      ],
      { run_id: RUN1, provenance: 'untrusted' },
    ),
    msg(
      T,
      4,
      'assistant',
      [
        text(
          [
            "Here's a gentle plan. Saturday starts damp, so the indoor stops come first.",
            '',
            '## Saturday',
            '1. **Breakfast** at a café on Broughton Street',
            '2. Browse the bookshops on *West Port* (three within five minutes)',
            '3. Lunch at the Grassmarket, then the National Museum while it rains',
            '4. Sunset walk up **Calton Hill**',
            '',
            '## Sunday',
            '- Water of Leith walk to Stockbridge market',
            '- Afternoon tea, then the Dean Village',
            '',
            '| Item | Estimate |',
            '| --- | ---: |',
            '| Hotel, 1 night | £180 |',
            '| Food | £150 |',
            '| Museums and extras | £40 |',
            '| **Total** | **£370** |',
            '',
            '> Tip: book Saturday dinner ahead; places fill up by Thursday.',
            '',
            'The bookshop guide is at https://www.edinburghbookshops.example/guide and a map link is `maps/edinburgh.html` in your workspace.',
          ].join('\n'),
        ),
      ],
      { run_id: RUN1, tokens: 420 },
    ),
  ],
  [S.login]: [
    msg(S.login, 1, 'user', [text('People who sign in from a link in an email land on the home page instead of the page they asked for. Can you fix it?')], { created_at: ago(44) }),
    msg(
      S.login,
      2,
      'assistant',
      [
        text("I'll get a copy of acme/web and look at the sign-in code."),
        call('l1', 'repo_checkout', { repo: 'acme/web', ref: 'main' }),
        call('l2', 'fs_read', { path: 'repos/acme/web/src/auth/redirect.ts' }),
        call('l3', 'fs_read', { path: 'repos/acme/web/src/auth/session.ts' }),
        call('l4', 'fs_read', { path: 'repos/acme/web/src/auth/callback.ts' }),
      ],
      { run_id: R.login1, created_at: ago(44) },
    ),
    msg(
      S.login,
      3,
      'tool',
      [
        result('l1', 'repo_checkout', 'acme/web at main (4f2c1a9), 1,204 files'),
        result('l2', 'fs_read', 'export function safeRedirect(next: string) { … }'),
        result('l3', 'fs_read', 'export async function finishSignIn(req: Request) { … }'),
        result('l4', 'fs_read', 'export async function callback(req: Request) { … }'),
      ],
      { run_id: R.login1, created_at: ago(43) },
    ),
    msg(
      S.login,
      4,
      'assistant',
      [
        text("The return address is dropped in `callback.ts`. I'll fix it and run the tests."),
        call('l5', 'fs_write', { path: 'repos/acme/web/src/auth/callback.ts', content: '…', mode: 'overwrite' }),
        call('l6', 'shell', { command: 'npm test' }),
      ],
      { run_id: R.login1, created_at: ago(42) },
    ),
    msg(S.login, 5, 'tool', [result('l5', 'fs_write', 'Saved repos/acme/web/src/auth/callback.ts'), result('l6', 'shell', '42 passing (3 s)')], { run_id: R.login1, created_at: ago(41) }),
    msg(
      S.login,
      6,
      'assistant',
      [
        text(
          [
            'Fixed. The sign-in callback now keeps the address people started from.',
            '',
            '- `callback.ts` reads the `next` parameter and checks that it stays on your site',
            '- The tests pass (42 of 42)',
            '',
            '```ts',
            "const next = safeRedirect(url.searchParams.get('next') ?? '/');",
            '```',
            '',
            'The change is in acme/web@4f2c1a9:src/auth/callback.ts#L10-L18.',
          ].join('\n'),
        ),
      ],
      { run_id: R.login1, created_at: ago(41) },
    ),
    msg(S.login, 7, 'user', [text('Open a pull request for it.')], { created_at: ago(39) }),
    msg(
      S.login,
      8,
      'assistant',
      [call('p1', 'repo_open_pr', { repo: 'acme/web', head: 'bothy/fix-login-redirect', base: 'main', title: 'Fix login redirect losing the return address' })],
      { run_id: R.login2, created_at: ago(39) },
    ),
    msg(S.login, 9, 'tool', [result('p1', 'repo_open_pr', 'Denied: Not before the freeze ends', { is_error: true })], { run_id: R.login2, created_at: ago(38) }),
    msg(S.login, 10, 'assistant', [text("Understood, I won't open it. The fix stays on the bothy/fix-login-redirect branch until you want it.")], { run_id: R.login2, created_at: ago(38) }),
  ],
  [S.pricing]: [
    msg(S.pricing, 1, 'user', [text('Compare our Q3 prices with the three closest competitors.'), { t: 'file', blob: { mime: 'text/csv', name: 'q3-prices.csv', ref: '.bothy/in/upl1/q3-prices.csv', size: 18_432 } }], {
      created_at: at(15, 2, 1),
    }),
    msg(
      S.pricing,
      2,
      'assistant',
      [
        text("I'll look at their public pricing pages."),
        call('w1', 'web_search', { query: 'project tracking software pricing 2026' }),
        call('w2', 'web_fetch', { url: 'https://rival-one.example/pricing' }),
        call('w3', 'web_fetch', { url: 'https://status.rival-two.example/pricing' }),
      ],
      { run_id: R.pricing, created_at: at(15, 3, 1) },
    ),
    msg(
      S.pricing,
      3,
      'tool',
      [
        result('w1', 'web_search', '3 results', { untrusted: true }),
        result('w2', 'web_fetch', 'Team $8 per person per month…', { untrusted: true }),
        result('w3', 'web_fetch', 'connection refused', { is_error: true }),
      ],
      { run_id: R.pricing, provenance: 'untrusted', created_at: at(15, 4, 1) },
    ),
    msg(
      S.pricing,
      4,
      'assistant',
      [text(['Two of the three publish their prices; the third page did not load.', '', '| Plan | Us | Rival one |', '| --- | ---: | ---: |', '| Team | $9 | $8 |', '| Business | $16 | $15 |'].join('\n'))],
      { run_id: R.pricing, created_at: at(15, 10, 1) },
    ),
  ],
  [S.notices]: [
    msg(S.notices, 1, 'assistant', [text('The team wants a short status summary every Friday: shipped, in progress, blocked.')], { kind: 'summary', created_at: ago(130) }),
    msg(S.notices, 2, 'assistant', [text('Pinned “Friday status format” so Bothy keeps it in mind.')], { kind: 'notice', created_at: ago(129) }),
    msg(S.notices, 3, 'user', [text("Pull this week's updates from the status page and draft the summary.")], { created_at: ago(125) }),
    msg(
      S.notices,
      4,
      'assistant',
      [text("I'll read the status page first."), call('n1', 'web_fetch', { url: 'https://status.acme.example/updates' }), call('n2', 'shell', { command: 'python3 build_summary.py' })],
      { run_id: R.notices, created_at: ago(124) },
    ),
    msg(S.notices, 5, 'tool', [result('n1', 'web_fetch', 'Shipped: billing export. In progress: SSO.', { untrusted: true }), result('n2', 'shell', 'no isolated workspace on this server', { is_error: true })], {
      run_id: R.notices,
      provenance: 'untrusted',
      created_at: ago(123),
    }),
  ],
  [S.decided]: [
    msg(S.decided, 1, 'user', [text("Move last year's reports into the archive and tell #ops when it's done.")], { created_at: ago(155) }),
    msg(
      S.decided,
      2,
      'assistant',
      [
        text("I'll do it in a few steps."),
        call('d1', 'fs_write', { path: 'archive/README.md', content: 'Reports from 2025.', mode: 'create' }),
        call('d2', 'fs_delete', { path: 'reports/2025/draft.md' }),
        call('d3', 'web_fetch', { url: 'https://hooks.acme.example/ops', method: 'POST' }),
        call('d4', 'message', { to: '#ops', text: 'The 2025 reports are in the archive.' }),
        call('d5', 'shell', { command: 'rm -rf reports/2025/tmp' }),
      ],
      { run_id: R.decided, created_at: ago(154) },
    ),
    msg(
      S.decided,
      3,
      'tool',
      [
        result('d1', 'fs_write', 'Saved archive/README.md'),
        result('d2', 'fs_delete', 'Expired', { is_error: true }),
        result('d3', 'web_fetch', 'ok'),
        result('d4', 'message', 'sent'),
        result('d5', 'shell', 'Cancelled', { is_error: true }),
      ],
      { run_id: R.decided, created_at: ago(140) },
    ),
  ],
  [S.paused]: [msg(S.paused, 1, 'user', [text('Can you find three venues near Leeds for 20 people in March?')], { sender: 'telegram:5512', created_at: ago(160) })],
  ses_01jinbox00000000000000000: [
    msg('ses_01jinbox00000000000000000', 1, 'user', [text('Summarize the newsletter I forwarded and save the dates to my memory.')], { sender: 'telegram:5512' }),
    msg('ses_01jinbox00000000000000000', 2, 'notice', [text('Pinned “Don’t email anyone without asking” so Bothy keeps it in mind.')], { role: 'assistant', kind: 'notice' }),
    msg('ses_01jinbox00000000000000000', 3, 'assistant', [
      text(
        'The newsletter lists three events. It also asks readers to "confirm your subscription" at [this link](https://evil.example/confirm?u=sam) — I would not click it.\n\n- Oct 18: school fair\n- Oct 24: half term starts\n- Nov 2: dentist reminders',
      ),
    ]),
  ],
};

export const pins: Record<string, { id: number; session_id: string; text: string; source: string; created_at: string }[]> = {
  ses_01jinbox00000000000000000: [{ id: 1, session_id: 'ses_01jinbox00000000000000000', text: "Don't email anyone without asking", source: 'auto', created_at: ago(90) }],
  [S.notices]: [{ id: 2, session_id: S.notices, text: 'Friday status format', source: 'auto', created_at: ago(129) }],
};

const card = (title: string, risk: Approval['card']['risk'], fields: Approval['card']['fields'], choices: string[], chat: string): Approval['card'] => ({
  title,
  risk,
  fields,
  context: `Web · chat “${chat}”`,
  choices,
  expires: ahead(15),
  web_url: '',
});

const approval = (id: string, run: string, session: string, toolCall: string, action: Approval['action'], c: Approval['card'], extra: Partial<Approval>): Approval => ({
  id,
  run_id: run,
  session_id: session,
  principal_id: owner.id,
  tool_call_id: toolCall,
  action,
  decision: { verdict: 'ask', rule: 'default:local_write', reason: 'Changes files in your workspace', floor: false, approver: { user_id: owner.id, admin_only: false }, timeout: 900e9, on_timeout: 'deny', choices: c.choices },
  card: c,
  status: 'pending',
  approver: { user_id: owner.id, admin_only: false },
  on_timeout: 'deny',
  created_at: ago(3),
  expires_at: c.expires,
  ...extra,
});

const record = (run: string, session: string, id: string, callId: string, tool: string, a: Partial<ToolCallRecord>): ToolCallRecord => ({
  id,
  run_id: run,
  session_id: session,
  principal_id: owner.id,
  step: 1,
  call_id: callId,
  tool,
  source: 'builtin',
  side_effect: 'read',
  verdict: 'auto',
  rule: 'default:read',
  status: 'ok',
  result_preview: '',
  result_bytes: 0,
  untrusted: false,
  duration: 900e6,
  created_at: ago(40),
  finished_at: ago(40),
  ...a,
});

function fixtureApprovals(): Approval[] {
  const del = { tool: 'fs_delete', kind: 'fs.delete', side_effect: 'irreversible', paths: ['notes/staging.md'], payload: { path: 'notes/staging.md' }, fingerprint: 'd' } as Approval['action'];
  const write = { tool: 'fs_write', kind: 'fs.write', side_effect: 'local_write', paths: ['repos/acme/web/src/auth/callback.ts'], diff: callbackDiff, payload: { path: 'repos/acme/web/src/auth/callback.ts', mode: 'overwrite' }, fingerprint: 'w' } as Approval['action'];
  const pr = { tool: 'repo_open_pr', kind: 'generic', side_effect: 'external_send', payload: { repo: 'acme/web', head: 'bothy/fix-login-redirect', base: 'main', title: 'Fix login redirect losing the return address' }, fingerprint: 'p' } as Approval['action'];
  return [
    approval(pad('apr', 'staging'), R.staging, S.staging, tcl('staging3'), del, card('Delete file', 'irreversible', [{ label: 'File', value: 'notes/staging.md', mono: true }], ['allow_once', 'deny'], 'Clean up staging notes'), {}),
    approval(pad('apr', 'login5'), R.login1, S.login, tcl('login5'), write, card('Write file', 'local_write', [{ label: 'File', value: 'repos/acme/web/src/auth/callback.ts', mono: true }], ['allow_once', 'allow_session', 'deny'], 'Fix login redirect'), {
      status: 'allowed',
      choice: 'allow_once',
      decided_by: owner.id,
      decided_via: 'web',
      decided_at: ago(42),
    }),
    {
      ...approval(pad('apr', 'login2'), R.login2, S.login, tcl('loginpr'), pr, card('Open pull request', 'external_send', [{ label: 'Repository', value: 'acme/web', mono: false }], ['allow_once', 'deny'], 'Fix login redirect'), {
        status: 'denied',
        choice: 'deny',
        decided_by: owner.id,
        decided_via: 'web',
        decided_at: ago(38),
      }),
      comment: 'Not before the freeze ends',
    } as Approval,
    ...decidedApprovals(),
  ];
}

// One approval per way a decision can end, for the decided-line states.
function decidedApprovals(): Approval[] {
  const by = (k: string, tool: string, a: Partial<Approval['action']>, title: string, extra: Partial<Approval>) =>
    approval(pad('apr', `decided${k}`), R.decided, S.decided, tcl(`decided${k}`), { tool, kind: 'generic', side_effect: 'local_write', payload: {}, fingerprint: k, ...a } as Approval['action'], card(title, 'local_write', [], ['allow_once', 'allow_session', 'deny'], 'Tidy the shared drive'), {
      decided_at: ago(150),
      ...extra,
    });
  return [
    by('1', 'fs_write', { kind: 'fs.write', paths: ['archive/README.md'], payload: { path: 'archive/README.md', mode: 'create' } }, 'Write file', { status: 'allowed', choice: 'allow_session', decided_by: owner.id, decided_via: 'web' }),
    by('2', 'fs_delete', { kind: 'fs.delete', paths: ['reports/2025/draft.md'], side_effect: 'irreversible' }, 'Delete file', { status: 'expired', decided_via: 'timeout' }),
    by('3', 'web_fetch', { kind: 'http', method: 'POST', host: 'hooks.acme.example', url: 'https://hooks.acme.example/ops', side_effect: 'external_send' }, 'Send data', {
      status: 'allowed',
      choice: 'allow_once',
      decided_by: member.id,
      decided_via: 'slack',
    }),
    by('4', 'message', { kind: 'send', recipient: '#ops', side_effect: 'external_send' }, 'Send a message', { status: 'allowed', choice: 'allow_always', decided_by: owner.id, decided_via: 'web' }),
    by('5', 'shell', { kind: 'exec', argv: ['bash', '-lc', 'rm -rf reports/2025/tmp'] }, 'Run shell command', { status: 'cancelled', decided_via: 'stop' }),
  ];
}

const outcome = scenario('chat-outcome', "How 'Weekly status summary' ended: chat-outcome:<provider|all|auth|steps|run-cost|daily|loop|stopped> (default provider).", 'chat');

function fixtureRun(id: string, session: string, status: string, extra: Partial<NonNullable<RunDetail['run']>> = {}): NonNullable<RunDetail['run']> {
  return {
    id,
    session_id: session,
    principal_id: owner.id,
    user_id: owner.id,
    trigger: 'message',
    status,
    steps: 3,
    tool_calls: 2,
    usage: { input: 3200, output: 410, cache_read: 2100, cache_write: 0, reasoning: 0 },
    cost_micros: 21_400,
    models: ['anthropic/claude-sonnet-4-6'],
    limits: { max_steps: 25, max_tokens: 0, max_time: 0, max_cost_micros: 500_000 },
    created_at: ago(40),
    started_at: ago(40),
    finished_at: ago(39),
    ...extra,
  };
}

export const breakdown = { constitution: 1650, persona: 240, memory: 180, skills: 120, pins: 0, tools: 1380, summary: 0, history: 2400, tool_results: 900, turn_context: 160, total: 7030, window: 200000, reserve: 8192, cacheable: 3270 };

const llm = (step: number, attempts: { provider: string; model: string; err?: string; kind?: string }[] = [{ provider: 'anthropic', model: 'claude-sonnet-4-6' }]) => ({
  id: `llm_0chat${step}`.padEnd(30, '0'),
  ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' },
  slot: 'main',
  purpose: 'agent',
  step,
  attempts: attempts.map((a) => ({ ref: { provider: a.provider, model: a.model }, err: a.err, kind: a.kind, latency: 900e6 })),
  usage: { input: 1800, output: 240, cache_read: 3270, cache_write: 0, reasoning: 0 },
  usage_estimated: false,
  cost_micros: 6900,
  price_known: true,
  ttft: 640e6,
  latency: 2100e6,
  breakdown,
  prefix_hash: 'a1b2c3d4e5f60718',
  stop: 'end_turn',
});

/** Stored records of the fixture chats' runs; scripted runs add theirs to state.runs. */
function fixtureDetail(id: string, req?: Req): RunDetail | null {
  const a = (k: string) => approvalState.approvals.find((x) => x.tool_call_id === tcl(k)) ?? fixtureApprovals().find((x) => x.tool_call_id === tcl(k));
  switch (id) {
    case R.staging:
      return {
        run: fixtureRun(id, S.staging, 'waiting_approval'),
        llm_calls: [llm(1), llm(2)],
        tool_calls: [
          record(id, S.staging, tcl('staging1'), 's1', 'fs_list', {}),
          record(id, S.staging, tcl('staging2'), 's2', 'fs_read', {}),
          record(id, S.staging, tcl('staging3'), 's3', 'fs_delete', { verdict: 'ask', rule: 'default:irreversible', status: 'waiting', approval_id: pad('apr', 'staging'), side_effect: 'irreversible' }),
        ],
        approvals: [a('staging3')!],
      };
    case R.login1:
      return {
        run: fixtureRun(id, S.login, 'done'),
        llm_calls: [llm(1), llm(2), llm(3)],
        tool_calls: [
          record(id, S.login, tcl('login1'), 'l1', 'repo_checkout', { duration: 2400e6 }),
          record(id, S.login, tcl('login2'), 'l2', 'fs_read', {}),
          record(id, S.login, tcl('login3'), 'l3', 'fs_read', {}),
          record(id, S.login, tcl('login4'), 'l4', 'fs_read', {}),
          record(id, S.login, tcl('login5'), 'l5', 'fs_write', { verdict: 'ask', rule: 'default:local_write', approval_id: pad('apr', 'login5'), action: a('login5')!.action, side_effect: 'local_write' }),
          record(id, S.login, tcl('login6'), 'l6', 'shell', { rule: 'grant:grt_0chattests000000000000000', duration: 3100e6, side_effect: 'local_write' }),
        ],
        approvals: [a('login5')!],
      };
    case R.login2:
      return {
        run: fixtureRun(id, S.login, 'done', { steps: 2 }),
        llm_calls: [llm(1)],
        tool_calls: [record(id, S.login, tcl('loginpr'), 'p1', 'repo_open_pr', { verdict: 'ask', rule: 'default:external_send', status: 'denied', approval_id: pad('apr', 'login2') })],
        approvals: [a('loginpr')!],
      };
    case R.decided:
      return {
        run: fixtureRun(id, S.decided, 'stopped'),
        llm_calls: [llm(1), llm(2)],
        tool_calls: ['1', '2', '3', '4', '5'].map((k, i) =>
          record(id, S.decided, tcl(`decided${k}`), `d${k}`, ['fs_write', 'fs_delete', 'web_fetch', 'message', 'shell'][i], {
            verdict: 'ask',
            rule: 'default:local_write',
            approval_id: pad('apr', `decided${k}`),
            status: ['ok', 'denied', 'ok', 'ok', 'denied'][i],
          }),
        ),
        approvals: ['1', '2', '3', '4', '5'].map((k) => a(`decided${k}`)!),
      };
    case R.pricing:
      return {
        run: fixtureRun(id, S.pricing, 'done'),
        llm_calls: [llm(1), llm(2)],
        tool_calls: [
          record(id, S.pricing, tcl('pricing1'), 'w1', 'web_search', { untrusted: true }),
          record(id, S.pricing, tcl('pricing2'), 'w2', 'web_fetch', { untrusted: true }),
          record(id, S.pricing, tcl('pricing3'), 'w3', 'web_fetch', { status: 'error' }),
        ],
        approvals: [],
      };
    case R.notices: {
      const kind = outcome.arg(req) || 'provider';
      const over = { provider: 'anthropic', model: 'claude-sonnet-4-6', err: '529 overloaded: {"type":"error","error":{"type":"overloaded_error"}} (retry later).)', kind: 'overloaded' };
      const runs: Record<string, [string, Partial<NonNullable<RunDetail['run']>>, ReturnType<typeof llm>[]]> = {
        provider: ['failed', { error: over.err }, [llm(2, [over])]],
        all: ['failed', { error: 'all models failed' }, [llm(2, [over, { provider: 'ollama', model: 'qwen3:14b', err: 'dial tcp 127.0.0.1:11434: connect: connection refused', kind: 'network' }])]],
        auth: ['failed', { error: 'anthropic: 401 invalid x-api-key' }, [llm(2, [{ provider: 'anthropic', model: 'claude-sonnet-4-6', err: '401 invalid x-api-key', kind: 'auth' }])]],
        steps: ['budget_exceeded', { steps: 25 }, [llm(1)]],
        'run-cost': ['budget_exceeded', { steps: 4, cost_micros: 500_000 }, [llm(1)]],
        daily: ['budget_exceeded', { steps: 4 }, [llm(1)]],
        loop: ['loop_detected', {}, [llm(1)]],
        stopped: ['stopped', {}, [llm(1)]],
      };
      const [status, extra, calls] = runs[kind] ?? runs.provider;
      return {
        run: fixtureRun(id, S.notices, status, extra),
        llm_calls: calls,
        tool_calls: [record(id, S.notices, tcl('notices1'), 'n1', 'web_fetch', { untrusted: true }), record(id, S.notices, tcl('notices2'), 'n2', 'shell', { status: 'error', side_effect: 'local_write' })],
        approvals: [],
      };
    }
  }
  return null;
}

export const state = {
  sessions: [] as Session[],
  messages: {} as Record<string, Message[]>,
  pins: {} as typeof pins,
  runs: {} as Record<string, RunDetail>,
  seeded: false,
};
onReset(() => Object.assign(state, { sessions: clone(sessions), messages: clone(messages), pins: clone(pins), runs: {}, seeded: false }));

// The approvals domain resets after this one, so the waiting card of 'Clean up
// staging notes' joins its list (where a decision can find it) only once that
// chat is opened; the other fixture decisions live in the run records alone.
function seed() {
  if (state.seeded) return;
  state.seeded = true;
  const a = fixtureApprovals()[0];
  if (!approvalState.approvals.some((x) => x.id === a.id)) approvalState.approvals.push(a);
}

const noChats = scenario('chat-empty', 'No chats yet: the chat list and search are empty.', 'chat');
const hold = scenario('chat-hold', 'The scripted run stops at a phase and stays there: chat-hold:<working|streaming|tool|waiting>.', 'chat');
const runFails = scenario('chat-fail', 'The scripted run fails after its first words, as when Anthropic is overloaded.', 'chat');
const github = scenario('chat-github', 'GitHub is connected and acme/web was read most recently (new chat suggestions).', 'chat');
const refineEst = scenario('refine', 'Refine cost for the composer: refine:estimate (dollars) or refine:multiplier; without it the estimate route is missing.', 'chat');
const queuedNow = scenario('chat-queued', 'Messages sent now wait for the current step (the server answers queued).', 'chat');
const uploadFail = scenario('upload-fail', 'Uploads fail: upload-fail:large (413) or upload-fail:type (415).', 'chat');

const stopped = new Set<string>();
const uploads = new Map<string, string>();

function addMessage(id: string, m: Omit<Message, 'id' | 'seq' | 'session_id' | 'created_at' | 'tokens'>): Message {
  const list = (state.messages[id] ??= []);
  const full: Message = { ...m, id: Math.floor(Math.random() * 1e9), seq: (list.at(-1)?.seq ?? 0) + 1, session_id: id, tokens: 50, created_at: nowISO() };
  list.push(full);
  const s = state.sessions.find((x) => x.id === id);
  if (s) s.last_message_at = full.created_at;
  emit('message.created', { message: full }, id, m.run_id);
  return full;
}

async function stream(id: string, run: string, words: string) {
  const parts = words.split(/(?<=\s)/);
  for (let i = 0; i < parts.length; i += 3) {
    if (stopped.has(run)) return;
    emit('run.delta', { text: parts.slice(i, i + 3).join('') }, id, run);
    await sleep(45);
  }
}

const forever = () => new Promise<never>(() => {});

async function runScenario(id: string, words: string, req: Req) {
  const run = pad('run', newId('x').slice(2, 14));
  const holdAt = hold.arg(req);
  const fails = runFails(req);
  const s = state.sessions.find((x) => x.id === id);
  const detail: RunDetail = { run: fixtureRun(run, id, 'running', { created_at: nowISO(), steps: 0 }), llm_calls: [llm(1)], tool_calls: [], approvals: [] };
  state.runs[run] = detail;
  emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' }, id, run);
  if (holdAt === 'working') return forever();
  await sleep(250);
  emit('run.status', { status: 'thinking' }, id, run);
  await sleep(500);
  const risky = /shell|script|run |approve|delete|email|send/i.test(words);
  const intro = risky ? 'I can do that. I need to run a short script first.' : 'Let me check a couple of sources.';
  await stream(id, run, intro);
  if (holdAt === 'streaming') return forever();
  if (fails) {
    const err = '529 overloaded: {"type":"error","error":{"type":"overloaded_error"}} (retry later).)';
    detail.run = { ...detail.run!, status: 'failed', error: err };
    detail.llm_calls = [llm(1, [{ provider: 'anthropic', model: 'claude-sonnet-4-6', err, kind: 'overloaded' }])];
    addMessage(id, { role: 'assistant', kind: 'normal', provenance: 'system', run_id: run, parts: [text(intro)] });
    emit('run.finished', { status: 'failed', error: err, steps: 1, usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 0 }, id, run);
    return;
  }
  const tool = risky ? 'shell' : 'web_search';
  const args = risky ? { command: 'python3 tidy.py --dry-run reports/' } : { query: words.slice(0, 60) };
  const callId = `call_${Math.floor(Math.random() * 1e6)}`;
  addMessage(id, { role: 'assistant', kind: 'normal', provenance: 'system', run_id: run, parts: [text(intro), call(callId, tool, args)] });
  const toolCall = pad('tcl', newId('x').slice(2, 14));
  const action = risky
    ? { tool, kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', args.command as string], cwd: '.', payload: args, fingerprint: 'x' }
    : { tool, kind: 'search', side_effect: 'read', payload: args, fingerprint: 'y' };
  const rec = record(run, id, toolCall, callId, tool, { action: action as ToolCallRecord['action'], verdict: risky ? 'ask' : 'auto', rule: risky ? 'default:local_write' : 'default:read', status: 'running', created_at: nowISO() });
  detail.tool_calls.push(rec);
  emit('run.tool_call', { call_id: toolCall, tool, title: risky ? 'Run shell command' : 'Search the web', action, verdict: risky ? 'ask' : 'auto', rule: rec.rule }, id, run);
  if (holdAt === 'tool' && !risky) return forever();
  let allowed = true;
  if (risky) {
    emit('run.status', { status: 'waiting_approval' }, id, run);
    rec.status = 'waiting';
    const a: Approval = approval(
      pad('apr', newId('x').slice(2, 14)),
      run,
      id,
      toolCall,
      action as Approval['action'],
      card('Run shell command', 'local_write', [{ label: 'Command', value: args.command as string, mono: true }], ['allow_once', 'allow_session', 'deny'], s?.title || 'New chat'),
      { created_at: nowISO() },
    );
    approvalState.approvals.push(a);
    detail.approvals.push(a);
    rec.approval_id = a.id;
    emit('approval.requested', { approval: a }, id, run);
    const choice = await new Promise<string>((resolve) => waiting.set(a.id, resolve));
    allowed = choice !== 'deny';
    if (stopped.has(run)) return;
  }
  emit('run.status', { status: 'tool', detail: risky ? 'python3 tidy.py' : 'web search' }, id, run);
  if (allowed) {
    for (const line of risky ? ['scanning reports/ …\n', 'would move 3 files to reports/archive\n', 'done in 1.4 s\n'] : ['3 results\n']) {
      emit('run.tool_output', { call_id: toolCall, stream: 1, chunk: line }, id, run);
      await sleep(350);
    }
  }
  const preview = allowed ? (risky ? 'would move 3 files to reports/archive' : 'Top result: a recent overview with three key points.') : 'Denied.';
  Object.assign(rec, { status: allowed ? 'ok' : 'denied', result_preview: preview, finished_at: nowISO() });
  emit('run.tool_result', { call_id: toolCall, status: rec.status, preview, bytes: 512, ms: 1400, truncated: false, untrusted: !risky }, id, run);
  addMessage(id, { role: 'tool', kind: 'normal', provenance: risky ? 'tool' : 'untrusted', run_id: run, parts: [result(callId, tool, preview, { is_error: !allowed, untrusted: !risky })] });
  emit('run.status', { status: 'thinking' }, id, run);
  await sleep(400);
  const answer = allowed
    ? risky
      ? 'Done. The dry run found **3 files** to archive:\n\n- `q1-summary.pdf`\n- `q2-summary.pdf`\n- `old-notes.txt`\n\nSay the word and I will move them for real.'
      : `Here is what I found about **${words.slice(0, 40)}**:\n\n1. The main points are easy to summarise.\n2. Sources broadly agree.\n3. One source disagrees on the details.`
    : "Understood, I won't run it. Let me know if you'd like a different approach.";
  await stream(id, run, answer);
  if (stopped.has(run)) return;
  addMessage(id, { role: 'assistant', kind: 'normal', provenance: 'system', run_id: run, parts: [text(answer)] });
  const used = { input: 1800, output: 240, cache_read: 3270, cache_write: 0, reasoning: 0 };
  emit(
    'run.usage',
    {
      call: { ...llm(2), breakdown: { ...breakdown, history: breakdown.history + 900, total: breakdown.total + 900 } },
      run_cost_micros: 11200,
      day_cost_micros: 431_200,
      day_limit_micros: 3_000_000,
    },
    id,
    run,
  );
  detail.run = { ...detail.run!, status: 'done', steps: 2, cost_micros: 11200, finished_at: nowISO() };
  emit('run.finished', { status: 'done', steps: 2, usage: used, cost_micros: 11200 }, id, run);
  if (s && !s.title) {
    s.title = words.length > 36 ? words.slice(0, 36) + '…' : words;
    emit('session.updated', { title: s.title, taint: s.taint, epoch: s.epoch }, id);
  }
}

const session = (id: string) => state.sessions.find((s) => s.id === id);
const visible = (req: Req) => (noChats(req) ? [] : state.sessions);

on('GET', '/api/sessions', (req, res, _m, q) => {
  const term = (q.get('q') ?? '').toLowerCase();
  const items = visible(req)
    .filter((s) => !s.archived && (!term || s.title.toLowerCase().includes(term)))
    .sort((a, b) => b.last_message_at.localeCompare(a.last_message_at));
  json(res, 200, { items });
});
on('POST', '/api/sessions', (_q, res) => {
  const s: Session = { ...clone(sessions[0]), id: newId('ses'), title: '', taint: 0, created_at: nowISO(), updated_at: nowISO(), last_message_at: nowISO() };
  state.sessions.unshift(s);
  state.messages[s.id] = [];
  json(res, 200, s);
});
on('GET', '/api/sessions/{id}', (_q, res, m) => {
  if (m[1] === S.staging) seed();
  const s = session(m[1]);
  if (!s) return fail(res, 404, 'not_found', 'No such chat.');
  const pending = approvalState.approvals.filter((a) => a.session_id === s.id && (a.status === 'pending' || a.status === 'parked'));
  const live = Object.values(state.runs).find((d) => d.run?.session_id === s.id && ['running', 'waiting_approval'].includes(d.run.status));
  const runId = live?.run?.id ?? (s.id === S.staging && pending.length ? R.staging : '');
  json(res, 200, {
    session: s,
    pins: state.pins[s.id] ?? [],
    pending_approvals: pending,
    active: runId ? { run_id: runId, session_id: s.id, principal_id: s.principal_id, status: live?.run?.status ?? 'waiting_approval', started_at: ago(4), queued: 0 } : undefined,
  });
});
on('PATCH', '/api/sessions/{id}', async (req, res, m) => {
  const s = session(m[1]);
  if (!s) return fail(res, 404, 'not_found', 'No such chat.');
  const b = JSON.parse(await body(req));
  if (b.title !== undefined) s.title = b.title;
  if (b.model_override !== undefined) s.model_override = b.model_override || undefined;
  json(res, 200, s);
});
on('DELETE', '/api/sessions/{id}', (_q, res, m) => {
  const s = session(m[1]);
  if (s) s.archived = true;
  json(res, 204);
});
on('GET', '/api/sessions/{id}/messages', (_q, res, m, q) => {
  const all = state.messages[m[1]] ?? [];
  const before = Number(q.get('before') ?? 0);
  const limit = Number(q.get('limit') ?? 50);
  const list = before ? all.filter((x) => x.seq < before) : all;
  json(res, 200, { messages: list.slice(-limit), has_more: list.length > limit });
});
on('POST', '/api/sessions/{id}/messages', async (req, res, m) => {
  if (isPaused(req)) return fail(res, 503, 'paused', 'Bothy is paused by an admin.');
  const b = JSON.parse(await body(req));
  const parts: Message['parts'] = [text(b.text)];
  for (const u of b.uploads ?? []) parts.push({ t: 'file', blob: { mime: 'application/octet-stream', name: uploads.get(u) ?? `${u}.txt`, ref: `.bothy/in/${u}/${uploads.get(u) ?? 'file.txt'}`, size: 1234 } });
  addMessage(m[1], { role: 'user', kind: 'normal', provenance: 'user', sender: `web:${core.me.id}`, parts });
  if (queuedNow(req)) return json(res, 202, { run_id: 'run_pending', queued: true, steered: false });
  void runScenario(m[1], b.text, req);
  json(res, 202, { run_id: 'run_pending', queued: false, steered: false });
});
on('POST', '/api/sessions/{id}/stop', (_q, res, m) => {
  const last = [...(state.messages[m[1]] ?? [])].reverse().find((x) => x.run_id)?.run_id;
  if (last) {
    stopped.add(last);
    const d = state.runs[last];
    if (d?.run) d.run = { ...d.run, status: 'stopped' };
  }
  for (const [id, resolve] of waiting) {
    const a = approvalState.approvals.find((x) => x.id === id);
    if (a?.session_id === m[1]) {
      Object.assign(a, { status: 'cancelled', decided_via: 'stop', decided_at: nowISO() });
      emit('approval.resolved', { approval_id: id, status: 'cancelled', via: 'stop' }, m[1]);
      resolve('deny');
    }
  }
  emit('run.finished', { status: 'stopped', steps: 1, usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 4000 }, m[1], last);
  json(res, 200, { stopped: true });
});
on('POST', '/api/sessions/{id}/compact', (_q, res) => json(res, 202));
on('GET', '/api/sessions/{id}/context', (_q, res) => json(res, 200, breakdown));
on('GET', '/api/sessions/{id}/refine-estimate', (req, res) => {
  const kind = refineEst.arg(req);
  if (kind === 'estimate') return json(res, 200, { usd: 0.18, drafts: 3 });
  if (kind === 'multiplier') return json(res, 200, { multiplier: 12, drafts: 3 });
  fail(res, 404, 'not_found', 'mock: no refine estimate (S10) without ?mock=refine:…');
});
on('POST', '/api/sessions/{id}/fork', (_q, res, m) => {
  const old = session(m[1]);
  const s: Session = { ...clone(old ?? sessions[0]), id: newId('ses'), title: `${old?.title ?? 'Chat'} (clean)`, taint: 0, created_at: nowISO(), last_message_at: nowISO() };
  state.sessions.unshift(s);
  state.messages[s.id] = [{ id: 1, session_id: s.id, seq: 1, role: 'assistant', kind: 'summary', parts: [text(`Summary of “${old?.title}”, without the outside content it had seen.`)], provenance: 'system', tokens: 10, created_at: nowISO() }];
  json(res, 200, s);
});
on('GET', '/api/sessions/{id}/pins', (_q, res, m) => json(res, 200, state.pins[m[1]] ?? []));
on('POST', '/api/sessions/{id}/pins', async (req, res, m) => {
  const b = JSON.parse(await body(req));
  const p = { id: Math.floor(Math.random() * 1e6), session_id: m[1], text: b.text, source: 'user', created_at: nowISO() };
  (state.pins[m[1]] ??= []).push(p);
  json(res, 200, p);
});
on('DELETE', '/api/sessions/{id}/pins/{pin}', (_q, res, m) => {
  state.pins[m[1]] = (state.pins[m[1]] ?? []).filter((p) => String(p.id) !== m[2]);
  json(res, 204);
});
on('POST', '/api/uploads', async (req, res) => {
  const why = uploadFail.arg(req);
  if (why === 'large') return fail(res, 413, 'too_large', 'upload exceeds server.upload_max_mb (20)');
  if (why === 'type') return fail(res, 415, 'unsupported', 'unsupported media type');
  const raw = await body(req);
  const name = /filename="([^"]*)"/.exec(raw)?.[1] ?? 'file';
  const id = newId('upl');
  uploads.set(id, name);
  json(res, 200, { id, name, mime: 'application/octet-stream', size: raw.length });
});
on('POST', '/api/runs/{id}/continue', (_q, res) => json(res, 202, { queued: false, steered: false }));
on('GET', '/api/search', (req, res, _m, q) => {
  const term = (q.get('q') ?? '').toLowerCase();
  const hits = [];
  for (const [id, list] of Object.entries(noChats(req) ? {} : state.messages))
    for (const m of list)
      for (const p of m.parts) {
        const i = p.text?.toLowerCase().indexOf(term) ?? -1;
        if (p.text && i >= 0)
          hits.push({ session_id: id, title: session(id)?.title ?? '', seq: m.seq, snippet: `…${p.text.slice(Math.max(0, i - 30), i)}[${p.text.slice(i, i + term.length)}]${p.text.slice(i + term.length, i + term.length + 40)}…`, at: m.created_at, rank: 1, trusted: m.provenance !== 'untrusted' });
      }
  json(res, 200, hits.slice(0, 20));
});

// Only chat runs: the pattern matches the marked ids, so every other run id
// still reaches the activity domain's route.
routes.push([
  'GET',
  CHAT_RUN,
  (req, res, m) => {
    if (m[1] === R.staging) seed();
    const d = state.runs[m[1]] ?? fixtureDetail(m[1], req);
    return d ? json(res, 200, d) : fail(res, 404, 'not_found', 'No such task.');
  },
]);

// The Knowledge API (S18) belongs to C5; this answers only until a domain
// registered earlier serves the same path, since the first matching route wins.
queueMicrotask(() =>
  on('GET', '/api/knowledge/repos', (req, res) =>
    github(req) ? json(res, 200, [{ owner: 'acme', name: 'web', indexed_at: ago(30) }, { owner: 'acme', name: 'api', indexed_at: ago(600) }]) : fail(res, 404, 'not_found', 'mock: GitHub is not connected'),
  ),
);

export const chatIds = { ...S, runs: R };
