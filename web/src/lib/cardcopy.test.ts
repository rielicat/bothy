import { describe, expect, it } from 'vitest';
import type { Action, Approval, ApprovalVotes, SideEffect } from './api/types.gen';
import {
  busyLabel,
  cardCopy,
  decidable,
  decidedLine,
  denyAudience,
  expiryOf,
  fromTeam,
  humanize,
  isMulti,
  joinNames,
  originOf,
  plainWarnings,
  recipientName,
  segText,
  splitFiles,
  teamAsked,
  teamPerson,
  type CardCopy,
} from './cardcopy';

const me = 'usr_01jsam0000000000000000000';
const alex = 'usr_01jalex000000000000000000';

function act(tool: string, kind: string, se: SideEffect, payload: Record<string, unknown>, extra: Partial<Action> = {}): Action {
  return { tool, kind, side_effect: se, payload, fingerprint: 'f', ...extra };
}

function ap(a: Action | undefined, extra: Partial<Approval> = {}): Approval {
  return {
    id: 'apr_01jtest000000000000000000',
    run_id: 'run_1',
    session_id: 'ses_1',
    principal_id: me,
    tool_call_id: 'tcl_1',
    action: a,
    decision: { verdict: 'ask', rule: 'default:local_write', reason: 'Changes files', floor: false, approver: { user_id: me, admin_only: false }, timeout: 0, on_timeout: 'deny', choices: [] },
    card: { title: 'Card title', risk: a?.side_effect ?? 'local_write', fields: [], context: 'Web · session “Clean up staging notes”', choices: ['allow_once', 'allow_session', 'deny'], expires: '2099-01-01T00:00:00Z', web_url: '' },
    status: 'pending',
    approver: { user_id: me, admin_only: false },
    on_timeout: 'deny',
    created_at: '2026-10-14T10:00:00Z',
    expires_at: '2099-01-01T00:00:00Z',
    ...extra,
  };
}

const q = (c: CardCopy) => segText(c.question);
const labels = (c: CardCopy) => c.fields.map((f) => f.label);

const deleteDiff = '--- a/notes/staging.md\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-# Staging\n-Old\n';
const prDiff =
  'Repository: acme/web (private)\nBranch: b → main\n\ndiff --git a/a.ts b/a.ts\n--- a/a.ts\n+++ b/a.ts\n@@ -1,2 +1,2 @@\n-x\n+y\n z\ndiff --git a/b.ts b/b.ts\nnew file mode 100644\n--- /dev/null\n+++ b/b.ts\n@@ -0,0 +1,2 @@\n+one\n+two\n';

// docs/design/ui.md §8.2: question, primary button, consequence and fields per action.
describe('cardCopy, one row per action', () => {
  const rows: [string, Action, string, string, string | undefined, string[]][] = [
    ['fs_write create', act('fs_write', 'fs.write', 'local_write', { path: 'notes/a.md', content: 'a\nb', mode: 'create' }), 'Create notes/a.md?', 'Create file', undefined, ['Content']],
    [
      'fs_write overwrite',
      act('fs_write', 'fs.write', 'local_write', { path: 'notes/a.md', content: 'x', mode: 'overwrite' }, { diff: '--- a/notes/a.md\n+++ b/notes/a.md\n@@ -1 +1 @@\n-a\n+b\n' }),
      'Change notes/a.md?',
      'Save changes',
      undefined,
      ['Changes'],
    ],
    ['fs_write append', act('fs_write', 'fs.write', 'local_write', { path: 'log.md', content: 'x\ny\nz', mode: 'append' }), 'Add to log.md?', 'Add to file', undefined, ['Adds']],
    ['fs_delete', act('fs_delete', 'fs.delete', 'irreversible', { path: 'notes/staging.md' }, { diff: deleteDiff }), 'Delete notes/staging.md?', 'Delete file', "This can't be undone.", ['Content']],
    ['shell', act('shell', 'exec', 'local_write', { argv: ['bash', '-lc', 'npm test'], cwd: 'app' }, { argv: ['bash', '-lc', 'npm test'] }), 'Run this command?', 'Run command', undefined, ['Command', 'In']],
    ['python', act('python', 'exec', 'local_write', { argv: ['python3', 'x.py'], code: 'print(1)\nprint(2)' }), 'Run this Python script?', 'Run script', undefined, ['Script']],
    [
      'web_fetch non-GET',
      act('web_fetch', 'http', 'external_send', { url: 'https://api.example.com/x', body: { a: 1 } }, { url: 'https://api.example.com/x', host: 'api.example.com', method: 'POST' }),
      'Send data to api.example.com?',
      'Send data',
      undefined,
      ['Website', 'Data'],
    ],
    ['message', act('message', 'send', 'external_send', { text: 'Hello team' }, { recipient: 'slack:C1' }), 'Send a message to the Slack chat?', 'Send message', undefined, ['To', 'Message']],
    ['memory', act('memory', 'memory', 'local_write', { file: 'MEMORY', op: 'add', new: 'Releases go out on Tuesdays.' }), 'Remember this?', 'Remember', undefined, ['Note']],
    ['notes', act('notes', 'memory', 'local_write', { repo: 'acme/web' }, { diff: '--- a\n+++ b\n@@ -1 +1 @@\n-a\n+b\n' }), 'Update the notes for acme/web?', 'Update notes', undefined, ['Change']],
    [
      'schedule_create',
      act('schedule_create', 'schedule', 'admin', { name: 'Monday PRs', when: 'Every Monday at 9:00', prompt: 'List them', deliver: { channel: 'slack' } }),
      'Create the schedule “Monday PRs”?',
      'Create schedule',
      undefined,
      ['When', 'Sends to', 'Does'],
    ],
    ['schedule_delete', act('schedule_delete', 'schedule', 'admin', { id: 'sch_1', name: 'Morning briefing' }), 'Delete the schedule “Morning briefing”?', 'Delete schedule', "This can't be undone.", []],
    ['net_allow', act('net_allow', 'net', 'external_send', { host: 'pypi.org' }, { host: 'pypi.org', port: 443 }), 'Let Bothy reach pypi.org?', 'Allow for this chat', undefined, ['Website']],
    [
      'repo_open_pr',
      act('repo_open_pr', 'send', 'external_send', { repo: 'acme/web', head: 'bothy/fix', base: 'main', title: 'Fix it', body: '' }, { recipient: 'github:acme/web', diff: prDiff }),
      'Open a pull request in acme/web?',
      'Open pull request',
      undefined,
      ['Branch', 'Title', 'Changes'],
    ],
    // Without the pull request's title, "On" would only repeat the question.
    ['github_comment', act('github_comment', 'send', 'external_send', { repo: 'acme/web', number: 482, body: 'Ready.' }), 'Comment on #482 in acme/web?', 'Post comment', undefined, ['Comment']],
    ['github_comment with the title', act('github_comment', 'send', 'external_send', { repo: 'acme/web', number: 482, title: 'Fix login redirect', body: 'Ready.' }), 'Comment on #482 in acme/web?', 'Post comment', undefined, ['On', 'Comment']],
    [
      'release_deploy',
      act('release_deploy', 'release', 'irreversible', { environment: 'production', repo: 'acme/web', ref: 'main', workflow: 'deploy.yml', inputs: {} }),
      'Deploy acme/web to production?',
      'Approve deploy',
      'This updates production for everyone using it.',
      ['Environment', 'Version', 'Workflow', 'Approvals'],
    ],
    ['MCP tool', act('mcp__linear__create_issue', 'mcp', 'external_send', { title: 'Bug' }, { mcp_server: 'linear', mcp_tool: 'create_issue' }), 'Use Linear: Create issue?', 'Allow', undefined, ['Title']],
    ['admin class', act('config_set', 'generic', 'admin', { setting: 'Daily limit', value: '$20' }), "Change Bothy's settings?", 'Change settings', undefined, ['Setting', 'Value']],
    ['financial', act('pay', 'generic', 'financial', { amount: '$240.00', to: 'Northwind' }), 'Pay $240.00 to Northwind?', 'Pay $240.00', "Money will leave your account. This can't be undone.", ['To', 'Amount']],
    ['skill_install', act('skill_install', 'generic', 'local_write', { name: 'notes', source: 'https://skills.example.com/notes.zip' }), 'Install the “notes” skill?', 'Install skill', undefined, ['From']],
    ['unknown tool', act('report_export', 'generic', 'local_write', { name: 'notes' }), 'Let Bothy use “report export”?', 'Allow', undefined, ['Name']],
  ];

  it.each(rows)('%s', (_name, a, question, primary, consequence, fields) => {
    const c = cardCopy(ap(a));
    expect(q(c)).toBe(question);
    expect(c.primary).toBe(primary);
    expect(c.consequence).toBe(consequence);
    expect(labels(c)).toEqual(fields);
  });

  it('marks paths, hosts and repositories for verbatim mono rendering', () => {
    const c = cardCopy(ap(rows[3][1]));
    expect(c.question).toEqual(['Delete ', { code: 'notes/staging.md' }, '?']);
    expect(segText(c.ing)).toBe('deleting notes/staging.md');
    expect(segText(c.ask)).toBe('delete notes/staging.md');
  });

  it('folds only diffs and file contents; commands and messages are shown in full', () => {
    const shell = cardCopy(ap(rows[4][1]));
    expect(shell.fields[0]).toMatchObject({ value: 'npm test', mono: true });
    expect(shell.fields[0].show).toBeUndefined();
    const del = cardCopy(ap(rows[3][1]));
    expect(del.fields[0]).toMatchObject({ summary: '2 lines', show: { kind: 'code', content: '# Staging\nOld' } });
    const pr = cardCopy(ap(rows[13][1]));
    expect(pr.fields[2].summary).toBe('2 files, +3 −1');
    expect(pr.kind).toBe('pull_request');
  });

  it('names the chat a message goes to without its key, preferring the server words', () => {
    const msg = act('message', 'send', 'external_send', { text: 'Done' }, { recipient: 'telegram:42:' });
    expect(cardCopy(ap(msg)).fields[0]).toEqual({ label: 'To', value: 'Telegram chat 42' });
    const server = ap(msg, { card: { ...ap(msg).card, fields: [{ label: 'To', value: 'Telegram chat 42 (Ops)', mono: false }] } });
    expect(cardCopy(server).fields[0]).toEqual({ label: 'To', value: 'Telegram chat 42 (Ops)' });
  });

  it("says a chat's name when the server knows it, keeping the id beside it", () => {
    const msg = act('message', 'send', 'external_send', { text: 'Done' }, { recipient: 'telegram:5512:' });
    const named = ap(msg, { card: { ...ap(msg).card, fields: [{ label: 'To', value: '“Ops” on Telegram (chat 5512)', mono: false }], warnings: ['First contact with telegram:5512:.'] } });
    const c = cardCopy(named);
    expect(segText(c.question)).toBe('Send a message to “Ops” on Telegram?');
    expect(c.fields[0]).toEqual({ label: 'To', value: '“Ops” on Telegram (chat 5512)' });
    expect(c.warnings).toEqual(['Bothy hasn\'t sent anything to “Ops” on Telegram before.']);
    expect(recipientName('telegram:5512:', 'Ops')).toBe('“Ops” on Telegram (chat 5512)');
    expect(recipientName('slack:C1:17.2', 'eng')).toBe('“eng” on Slack (chat C1, thread 17.2)');
  });

  it('gives inbox rows a title that tells two requests apart', () => {
    const row = (a: Action) => segText(cardCopy(ap(a)).row ?? cardCopy(ap(a)).question);
    expect(row(act('shell', 'exec', 'local_write', { argv: ['bash', '-lc', 'npm test'] }, { argv: ['bash', '-lc', 'npm test'] }))).toBe('Run npm test?');
    expect(row(act('memory', 'memory', 'local_write', { op: 'add', new: 'Releases go out on Tuesdays.' }))).toBe('Remember “Releases go out on Tuesdays.”?');
    expect(row(act('message', 'send', 'external_send', { text: 'Hi' }, { recipient: 'telegram:42:' }))).toBe('Send a message to Telegram chat 42?');
    // Builders without a row title fall back to the question.
    expect(row(act('fs_delete', 'fs.delete', 'irreversible', { path: 'notes/a.md' }))).toBe('Delete notes/a.md?');
  });

  it('says how many lines a notes change touches before Show', () => {
    const notes = act('notes', 'memory', 'local_write', { repo: 'acme/web', op: 'replace' }, { diff: '--- a/n\n+++ b/n\n@@ -1,2 +1,2 @@\n same\n-old line\n+new line\n' });
    expect(cardCopy(ap(notes)).fields[0]).toMatchObject({ label: 'Change', summary: '1 line changed' });
  });

  it('says where schedule results really go: the web for accounts, the teammate or channel chat otherwise', () => {
    const sendsTo = (payload: Record<string, unknown>) => cardCopy(ap(act('schedule_create', 'schedule', 'admin', { name: 'Digest', prompt: 'x', ...payload }))).fields[1].value;
    expect(sendsTo({ owner: 'Sam Patel', owner_kind: 'user' })).toBe('Bothy on the web');
    expect(sendsTo({ owner: 'Maya Chen', usual_chat: 'slack', owner_kind: 'person' })).toBe("Maya Chen's messages on Slack");
    expect(sendsTo({ owner: '#eng', usual_chat: 'slack', owner_kind: 'space' })).toBe('#eng on Slack');
    expect(sendsTo({ deliver: { channel: 'web' } })).toBe('Bothy on the web');
    expect(sendsTo({ deliver: { channel: 'slack', chat_id: 'C1' } })).toBe('Slack');
  });

  it('uses red only for actions that cannot be undone or spend money, never for deploys', () => {
    const tone = (r: (typeof rows)[number]) => cardCopy(ap(r[1])).tone;
    const red = rows.filter((r) => tone(r) === 'destructive').map((r) => r[0]);
    expect(red).toEqual(['fs_delete', 'schedule_delete', 'financial']);
    expect(tone(rows.find((r) => r[0] === 'release_deploy')!)).toBe('default');
    const irreversibleMcp = act('mcp__linear__delete', 'mcp', 'irreversible', {}, { mcp_server: 'linear', mcp_tool: 'delete' });
    expect(cardCopy(ap(irreversibleMcp))).toMatchObject({ tone: 'destructive', consequence: "This can't be undone." });
  });

  it('describes the narrow permission "Allow for this chat" grants', () => {
    expect(cardCopy(ap(rows[1][1])).grant).toEqual(['changes to files in ', { code: 'notes/' }]);
    expect(segText(cardCopy(ap(rows[7][1])).grant)).toBe('sending messages to this Slack chat');
    expect(segText(cardCopy(ap(rows[12][1])).grant)).toBe('contacting pypi.org');
  });

  it('says what a grant that pins the whole request is about', () => {
    const g = (a: Action) => segText(cardCopy(ap(a)).grant);
    expect(g(act('memory', 'memory', 'local_write', { op: 'add', new: 'x' }))).toBe('remembering this exact note');
    expect(g(act('notes', 'memory', 'local_write', { repo: 'acme/web' }))).toBe('making this exact change to the notes for acme/web');
    expect(g(act('mcp__linear__create_issue', 'mcp', 'external_send', { title: 't' }, { mcp_server: 'linear', mcp_tool: 'create_issue' }))).toBe('using Linear: Create issue with these exact details');
  });

  it('prefers the server copy once the card carries it (S2)', () => {
    const a = ap(rows[3][1]);
    a.card = { ...a.card, question: 'Remove notes/staging.md?', primary_label: 'Remove', consequence: 'Gone for good.', tone: 'default', warnings: ['Plain warning.'] };
    const c = cardCopy(a);
    expect(q(c)).toBe('Remove notes/staging.md?');
    expect(c).toMatchObject({ primary: 'Remove', consequence: 'Gone for good.', tone: 'default', warnings: ['Plain warning.'] });
  });

  it('falls back to the card fields when an approval has no Action', () => {
    const a = ap(undefined, { card: { ...ap(undefined).card, title: 'Run shell command', fields: [{ label: 'Tool', value: 'shell', mono: true }, { label: 'Command', value: 'ls', mono: true }] } });
    const c = cardCopy(a);
    expect(q(c)).toBe('Run shell command?');
    expect(labels(c)).toEqual(['Command']);
  });
});

describe('plainWarnings', () => {
  it('rewrites the gateway warnings in plain words', () => {
    const w = (warnings: string[], a?: Action) => plainWarnings(ap(a, { card: { ...ap(a).card, warnings } }));
    expect(w(['First contact with api.example.com.'], act('web_fetch', 'http', 'external_send', {}))).toEqual(["Bothy hasn't sent data to api.example.com before."]);
    expect(w(['First contact with github:acme/web.'], act('repo_open_pr', 'send', 'external_send', {}))).toEqual(['This is the first pull request Bothy opens in acme/web for you.']);
    const pr = act('repo_open_pr', 'send', 'external_send', {});
    expect(plainWarnings(ap(pr, { principal_id: 'spc_1', card: { ...ap(pr).card, warnings: ['First contact with github:acme/web.'] } }))).toEqual([
      'This is the first pull request Bothy opens in acme/web for this group chat.',
    ]);
    expect(w(['First contact with telegram:55.'], act('message', 'send', 'external_send', {}, { recipient: 'telegram:55' }))).toEqual(["Bothy hasn't sent anything to Telegram chat 55 before."]);
    expect(w(['First time sending to this recipient.'], act('message', 'send', 'external_send', {}, { recipient: 'slack:C1:17.2' }))).toEqual(["Bothy hasn't sent anything to Slack chat C1, thread 17.2 before."]);
    expect(w(['This session has seen private data and untrusted content; anything sent out could leak it.'])).toEqual([
      'This chat has seen private information and content from outside. Check carefully before letting anything leave.',
    ]);
    expect(w(['Started by a message from an untrusted sender.'])).toEqual(['This request came from content Bothy read (a web page, repository or message), not from a person.']);
  });
});

describe('originOf', () => {
  it('reads the chat and app from the context line, and never shows an id', () => {
    expect(originOf(ap(undefined, { card: { ...ap(undefined).card, context: 'Telegram · DM · session “Q3 report”' } }))).toMatchObject({ app: 'Telegram', title: 'Q3 report', group: false });
    expect(originOf(ap(undefined, { card: { ...ap(undefined).card, context: 'Slack · group · session “Release”' } }))).toMatchObject({ app: 'Slack', group: true });
    expect(originOf(ap(undefined, { card: { ...ap(undefined).card, context: 'Web · session “ses_01jabcdefgh”' } })).title).toBeUndefined();
    expect(originOf(ap(undefined)).app).toBeUndefined();
    // A context line that doesn't start with an app names none.
    expect(originOf(ap(undefined, { card: { ...ap(undefined).card, context: 'From “Login bug”' } })).app).toBeUndefined();
  });
});

describe('expiry and buttons', () => {
  const now = Date.parse('2026-10-14T10:00:00Z');
  it('says how long is left, urgently under two minutes, and never counts down a parked request', () => {
    const at = (min: number) => new Date(now + min * 60_000).toISOString();
    expect(expiryOf(ap(undefined, { card: { ...ap(undefined).card, expires: at(15) } }), now)).toEqual({ text: 'Expires in 15 min', urgent: false });
    expect(expiryOf(ap(undefined, { card: { ...ap(undefined).card, expires: at(1.5) } }), now)).toEqual({ text: 'Expires in 2 min', urgent: true });
    expect(expiryOf(ap(undefined, { card: { ...ap(undefined).card, expires: '0001-01-01T00:00:00Z' }, expires_at: at(0.5) }), now)?.text).toBe('Expires in less than a minute');
    expect(expiryOf(ap(undefined, { status: 'parked' }), now)?.text).toBe('Bothy stopped waiting. Approve to let it carry on.');
  });

  it('labels a pressed button with its -ing form', () => {
    expect(['Delete file', 'Run command', 'Approve deploy', 'Allow for this chat', 'Deny', 'Save changes'].map(busyLabel)).toEqual([
      'Deleting…',
      'Running…',
      'Approving…',
      'Allowing…',
      'Denying…',
      'Saving…',
    ]);
    expect(['Forget', 'Let them chat as a guest', 'Stop reading'].map(busyLabel)).toEqual(['Forgetting…', 'Letting…', 'Stopping…']);
  });
});

describe('decidable', () => {
  const sam = { id: me, role: 'owner' };
  const member = { id: alex, role: 'member' };
  const multi = (extra: Partial<Approval> = {}) => ap(undefined, { approver: { user_id: '', admin_only: false, users: [me], roles: ['admin'], required: 2, requester: alex }, ...extra });
  const view = (v: Partial<ApprovalVotes>): ApprovalVotes => ({ approval_id: 'a', status: 'pending', required: 2, approvals: 0, votes: [], approvers: '', requester: alex, requester_name: 'Alex', can_vote: false, can_withdraw: false, ...v });

  it('lets only the named approver, or any admin for admin actions, decide one-person approvals', () => {
    expect(decidable(ap(undefined), sam)).toBe(true);
    expect(decidable(ap(undefined), member)).toBe(false);
    expect(decidable(ap(undefined, { approver: { user_id: '', admin_only: true } }), sam)).toBe(true);
    expect(decidable(ap(undefined, { approver: { user_id: '', admin_only: true } }), member)).toBe(false);
    expect(decidable(ap(undefined, { approver: { user_id: '', admin_only: false } }), sam)).toBe(false);
    expect(decidable(ap(undefined, { status: 'denied' }), sam)).toBe(false);
  });

  it("lets admins decide a chat app's request, and no member", () => {
    const team = ap(undefined, { principal_id: 'spc_1', approver: { user_id: '', admin_only: false, team: true, requester_identity: 'slack:U1' } });
    expect(decidable(team, sam)).toBe(true);
    expect(decidable(team, { id: me, role: 'admin' })).toBe(true);
    expect(decidable(team, member)).toBe(false);
    expect(isMulti(team)).toBe(false);
  });

  it('follows the vote state for multi-approver requests, and never the requester', () => {
    expect(decidable(multi(), sam, view({ can_vote: true }))).toBe(true);
    expect(decidable(multi(), sam, view({ can_vote: false, your_vote: 'allow_once' }))).toBe(false);
    expect(decidable(multi(), member)).toBe(false);
    expect(decidable(multi(), sam)).toBe(true);
    expect(decidable(multi({ votes: [{ approval_id: 'a', user_id: me, choice: 'allow_once', via: 'web', at: '' }] }), sam)).toBe(false);
  });
});

describe('requests from chat apps', () => {
  const team = { user_id: '', admin_only: false, team: true, requester_identity: 'slack:U1' };
  const person = { user: '', name: 'Maya Chen', app: 'slack', identity: 'slack:U1', handle: 'maya', kind: 'teammate' };
  const eng = { user: '', name: 'Maya Chen', app: 'slack', identity: 'slack:U1', chat: '#eng', kind: 'channel' };

  it('tells a team request by its approver or its principal', () => {
    expect(fromTeam(ap(undefined, { approver: team }))).toBe(true);
    // A teammate's deploy is voted on, yet still came from a chat app.
    expect(fromTeam(ap(undefined, { principal_id: 'spc_1', approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2 } }))).toBe(true);
    expect(fromTeam(ap(undefined))).toBe(false);
  });

  it('says who asked, and where', () => {
    expect(teamAsked(ap(undefined, { approver: team, requester: person }))).toBe('Maya Chen (@maya) asked on Slack.');
    expect(teamAsked(ap(undefined, { approver: team, requester: eng }))).toBe('Maya Chen asked in #eng on Slack.');
    expect(teamAsked(ap(undefined, { approver: team }))).toBe('');
  });

  it('says a schedule started it, never that someone asked', () => {
    expect(teamAsked(ap(undefined, { approver: team, requester: { ...person, scheduled: true } }))).toBe("From Maya Chen's schedule on Slack.");
    expect(teamAsked(ap(undefined, { approver: team, requester: { user: '', name: '#eng', app: 'slack', chat: '#eng', kind: 'channel', scheduled: true } }))).toBe('From #eng on Slack.');
  });

  it("names a teammate's chat on the standing choices, cut to fit, and never a channel's", () => {
    expect(teamPerson(ap(undefined, { approver: team, requester: person }))).toBe('Maya Chen');
    expect(teamPerson(ap(undefined, { approver: team, requester: { ...person, name: 'Maximilian Alexander Featherstonehaugh' } }))).toBe('Maximilian Alexander Fe…');
    expect(teamPerson(ap(undefined, { approver: team, requester: eng }))).toBe('');
    expect(teamPerson(ap(undefined, { approver: team, requester: { ...eng, name: '#eng', scheduled: true } }))).toBe('');
    expect(teamPerson(ap(undefined, { requester: person }))).toBe('');
  });

  it('says who reads a deny reason: the person, or nobody in a channel', () => {
    expect(denyAudience(ap(undefined, { approver: team, requester: person }))).toBe('Maya Chen will see this.');
    expect(denyAudience(ap(undefined, { approver: team, requester: eng }))).toBe("People in #eng won't see this, but Bothy will.");
    // A channel's schedule names the channel, not a person who would read it.
    expect(denyAudience(ap(undefined, { approver: team, requester: { user: '', name: '#eng', app: 'slack', chat: '#eng', kind: 'channel', scheduled: true } }))).toBe(
      "People in #eng won't see this, but Bothy will.",
    );
    expect(denyAudience(ap(undefined, { requester: person }))).toBe('');
  });

  it('says whose chat a standing choice covers, as its button did', () => {
    const write = act('fs_write', 'fs.write', 'local_write', { path: 'notes/a.md', mode: 'overwrite' });
    const decided = (choice: string, requester: Approval['requester'] = person) =>
      segText(decidedLine(ap(write, { approver: team, requester, status: 'allowed', choice, decided_by: me, decided_via: 'web', decided_at: '2026-10-14T10:42:00Z' }), { me, name: () => '' }).segs);
    expect(decided('allow_session')).toBe("You allowed changes to files in notes/ in Maya Chen's chat");
    expect(decided('allow_always')).toBe('You always allow changes to files in notes/ for Maya Chen');
    expect(decided('allow_session', eng)).toBe('You allowed changes to files in notes/ for this chat');
  });
});

describe('decidedLine', () => {
  const now = Date.parse('2026-10-14T12:00:00Z');
  const del = act('fs_delete', 'fs.delete', 'irreversible', { path: 'notes/staging.md' });
  const write = act('fs_write', 'fs.write', 'local_write', { path: 'notes/a.md', mode: 'overwrite' });
  const at = '2026-10-14T10:42:00Z';
  const names: Record<string, string> = { [alex]: 'Alex Kim', usr_maya: 'Maya Chen' };
  const line = (a: Approval, extra: Partial<Parameters<typeof decidedLine>[1]> = {}) => {
    const d = decidedLine(a, { me, name: (id) => names[id] ?? '', now, ...extra });
    return segText(d.segs) + (d.reason ? ` · “${d.reason}”` : '');
  };
  const decided = (a: Action, extra: Partial<Approval>) => ap(a, { decided_at: at, decided_via: 'web', decided_by: me, ...extra });

  it('says who decided what, in the tense of the record (§8.4)', () => {
    expect(line(decided(del, { status: 'allowed', choice: 'allow_once' }))).toBe('You approved deleting notes/staging.md');
    expect(line(decided(write, { status: 'allowed', choice: 'allow_session' }))).toBe('You allowed changes to files in notes/ for this chat');
    expect(line(decided(write, { status: 'allowed', choice: 'allow_always' }))).toBe('You always allow changes to files in notes/');
    expect(line(decided(del, { status: 'denied', choice: 'deny', comment: 'Not yet' }))).toBe('You denied deleting notes/staging.md · “Not yet”');
    expect(line(decided(del, { status: 'allowed', choice: 'allow_once', decided_by: alex, decided_via: 'slack' }))).toBe('Alex Kim approved in Slack');
    expect(line(decided(del, { status: 'denied', choice: 'deny', decided_by: alex, decided_via: 'slack' }))).toBe('Alex Kim denied in Slack');
    expect(line(decided(del, { status: 'allowed', choice: 'allow_once', decided_by: alex, decided_via: 'slack' }), { list: true })).toBe('Alex Kim approved deleting notes/staging.md');
    expect(line(decided(del, { status: 'expired' }))).toBe("Expired: Bothy didn't delete notes/staging.md");
    // The "You stopped Bothy." notice says why; the line says what was cancelled.
    expect(line(decided(del, { status: 'cancelled', decided_via: 'stop' }))).toBe("Cancelled: Bothy didn't delete notes/staging.md");
  });

  it('names every approver of a multi-approver request, and the one who denied', () => {
    const dep = act('release_deploy', 'release', 'irreversible', { environment: 'production', repo: 'acme/web' });
    const approver = { user_id: '', admin_only: false, users: [me], roles: ['admin'], required: 2, requester: me };
    const vote = (user_id: string, choice: string, comment?: string) => ({ approval_id: 'a', user_id, choice, via: 'web', at, comment });
    expect(line(decided(dep, { status: 'allowed', choice: 'allow_once', approver: { ...approver, requester: 'usr_x' }, votes: [vote(alex, 'allow_once'), vote('usr_maya', 'allow_once')] }))).toBe(
      'Alex Kim and Maya Chen approved deploying acme/web to production',
    );
    expect(line(decided(dep, { status: 'denied', choice: 'deny', approver: { ...approver, requester: 'usr_x' }, votes: [vote(alex, 'deny', 'Not before the freeze ends')] }))).toBe(
      'Alex Kim denied deploying acme/web to production · “Not before the freeze ends”',
    );
    expect(line(decided(dep, { status: 'denied', choice: 'deny', approver, votes: [vote(me, 'deny')] }))).toBe('Cancelled by you');
  });

  it('prints the time of day, and the date for older lines in a list', () => {
    const d = decidedLine(decided(del, { status: 'allowed', choice: 'allow_once' }), { me, name: () => '', now });
    expect(d.time).toMatch(/\d{1,2}:42/);
    const old = decidedLine(decided(del, { status: 'allowed', choice: 'allow_once', decided_at: '2026-10-02T10:42:00Z' }), { me, name: () => '', now, list: true });
    expect(old.time).toBe(new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' }).format(new Date('2026-10-02T10:42:00Z')));
  });
});

describe('helpers', () => {
  it('humanises tool names and joins names', () => {
    expect(humanize('create_issue')).toBe('Create issue');
    expect(humanize('dueDate')).toBe('Due date');
    expect(joinNames(['A'])).toBe('A');
    expect(joinNames(['A', 'B', 'C'])).toBe('A, B and C');
  });

  it('splits a pull request diff into files with their counts', () => {
    expect(splitFiles(prDiff).map((f) => [f.path, f.added, f.removed])).toEqual([
      ['a.ts', 1, 1],
      ['b.ts', 2, 0],
    ]);
  });
});
