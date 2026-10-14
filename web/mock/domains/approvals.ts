// Approvals (C4): pending and decided requests, and their decisions.
import type { Approval } from '../../src/lib/api/types.gen.ts';
import { ago, ahead, body, clone, core, emit, fail, ids, json, nowISO, on, onReset, owner, scenario } from './core.ts';

const card = (title: string, risk: Approval['card']['risk'], fields: { label: string; value: string; mono: boolean }[], extra: Partial<Approval['card']> = {}): Approval['card'] => ({
  title,
  risk,
  fields,
  context: 'Web · session “Weekend in Edinburgh”',
  choices: ['allow_once', 'allow_session', 'allow_always', 'deny'],
  expires: ahead(14),
  web_url: '',
  ...extra,
});

const decision = (rule: string, reason: string): Approval['decision'] => ({
  verdict: 'ask',
  rule,
  reason,
  floor: false,
  approver: { user_id: owner.id, admin_only: false },
  timeout: 15 * 60e9,
  on_timeout: 'deny',
  choices: ['allow_once', 'allow_session', 'allow_always', 'deny'],
});

export const approvals: Approval[] = [
  {
    id: 'apr_01jshell00000000000000000',
    run_id: ids.tripRun,
    session_id: ids.trip,
    principal_id: owner.id,
    tool_call_id: 'tcl_01jshell00000000000000000',
    decision: decision('default:local_write', 'Writes files in your workspace'),
    card: card('Run shell command', 'local_write', [
      { label: 'Command', value: "python3 build_itinerary.py --out maps/edinburgh.html --city 'Edinburgh'", mono: true },
      { label: 'Folder', value: 'trips/', mono: true },
    ]),
    status: 'pending',
    approver: { user_id: owner.id, admin_only: false },
    on_timeout: 'deny',
    created_at: ago(1),
    expires_at: ahead(14),
  },
  {
    id: 'apr_01jfetch00000000000000000',
    run_id: ids.briefRun,
    session_id: ids.brief,
    principal_id: owner.id,
    tool_call_id: 'tcl_01jfetch00000000000000000',
    decision: decision('first_contact:domain', 'First request to this site'),
    card: card(
      'Send a message',
      'external_send',
      [
        { label: 'Recipient', value: 'team‮gnp.ledom@example.com', mono: true },
        { label: 'URL', value: 'https://аpple-support.example/api/report', mono: true },
      ],
      {
        context: 'Schedule “Morning briefing” · parked',
        warnings: ['First time sending to this recipient', 'The session read untrusted web content'],
        choices: ['allow_once', 'deny'],
        diff: '--- a/notes.md\n+++ b/notes.md\n@@ -1,2 +1,3 @@\n # Notes\n-old line\n+new line\n+another line',
      },
    ),
    status: 'parked',
    approver: { user_id: owner.id, admin_only: false },
    on_timeout: 'park',
    created_at: ago(70),
    expires_at: ago(68),
    park_until: ahead(60 * 22),
  },
];

export const recentApprovals: Approval[] = [
  { ...approvals[0], id: 'apr_01jold1000000000000000000', status: 'allowed', choice: 'allow_session', decided_by: owner.id, decided_via: 'telegram', decided_at: ago(60 * 5), created_at: ago(60 * 5) },
  { ...approvals[0], id: 'apr_01jold2000000000000000000', status: 'denied', choice: 'deny', decided_by: owner.id, decided_via: 'web', decided_at: ago(60 * 30), created_at: ago(60 * 30) },
];

export const state = { approvals: [] as Approval[], recent: [] as Approval[] };
onReset(() => Object.assign(state, { approvals: clone(approvals), recent: clone(recentApprovals) }));

/** Runs waiting on a decision, by approval id; chat.ts parks its scripted run here. */
export const waiting = new Map<string, (choice: string) => void>();

const none = scenario('approvals-empty', 'Nothing waits for approval and nothing was decided.', 'approvals');

on('GET', '/api/approvals', (req, res, _m, q) => {
  if (none(req)) return json(res, 200, { items: [] });
  const status = q.get('status') ?? 'pending';
  json(res, 200, { items: status === 'recent' ? state.recent : state.approvals.filter((a) => a.status === status) });
});
on('GET', '/api/approvals/{id}', (_q, res, m) => {
  const a = [...state.approvals, ...state.recent].find((x) => x.id === m[1]);
  return a ? json(res, 200, a) : fail(res, 404, 'not_found', 'No such approval.');
});
on('POST', '/api/approvals/{id}', async (req, res, m) => {
  const a = state.approvals.find((x) => x.id === m[1]);
  if (!a) return fail(res, 404, 'not_found', 'No such approval.');
  if (a.status !== 'pending' && a.status !== 'parked') return fail(res, 409, 'conflict', 'Already decided.');
  const { choice } = JSON.parse(await body(req));
  Object.assign(a, { status: choice === 'deny' ? 'denied' : 'allowed', choice, decided_by: core.me.id, decided_via: 'web', decided_at: nowISO() });
  state.recent.unshift(a);
  emit('approval.resolved', { approval_id: a.id, status: a.status, choice, by: core.me.id, via: 'web' }, a.session_id, a.run_id);
  waiting.get(a.id)?.(choice);
  waiting.delete(a.id);
  json(res, 200, a);
});
