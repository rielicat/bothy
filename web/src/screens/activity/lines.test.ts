import { describe, expect, it } from 'vitest';
import type { Approval, AuditEntry, EgressRecord, ToolCallRecord } from '../../lib/api/types.gen';
import { actorName, resultWord, summarize, webLine } from './audit';
import { checkinFile, checkinText, every, grantSentence, listDate, nameFrom, nextRun } from './schedule';
import { decisionLine, stepLine, type Segment } from './steps';
import { clampLines, deliverTo, detailOutcome, taskView, type TaskLookups } from './tasks';

const text = (s: Segment[]) => s.map((x) => x.text).join('');
const call = (tool: string, status: string, action: Partial<NonNullable<ToolCallRecord['action']>> = {}) =>
  ({ id: 't', tool, status, verdict: 'auto', rule: '', action: { tool, kind: 'other', side_effect: 'read', fingerprint: '', payload: {}, ...action } }) as unknown as ToolCallRecord;

describe('task steps (docs/design/ui.md §10.2, §7.4)', () => {
  it('uses the past tense only once the result is in', () => {
    const del = { kind: 'fs.delete', paths: ['notes/staging.md'], payload: { path: 'notes/staging.md' } };
    expect(text(stepLine(call('fs_delete', 'waiting', del)).segments)).toBe('Waiting for approval to delete notes/staging.md');
    expect(text(stepLine(call('fs_delete', 'running', del)).segments)).toBe('Deleting notes/staging.md…');
    expect(text(stepLine(call('fs_delete', 'ok', del)).segments)).toBe('Deleted notes/staging.md');
    expect(text(stepLine(call('fs_delete', 'denied', del)).segments)).toBe("Didn't delete notes/staging.md");
    expect(text(stepLine(call('fs_delete', 'error', del)).segments)).toBe("Couldn't delete notes/staging.md");
  });

  it('shows paths, hosts and commands verbatim as code, prose as text', () => {
    const s = stepLine(call('shell', 'ok', { kind: 'exec', argv: ['npm', 'test'] })).segments;
    expect(s).toEqual([
      { text: 'Ran ', code: false },
      { text: 'npm test', code: true },
    ]);
    expect(text(stepLine(call('web_search', 'ok', { payload: { query: 'pricing' } })).segments)).toBe('Searched the web for “pricing”');
  });

  it('never names a tool by its id', () => {
    expect(text(stepLine(call('mcp__linear__create_issue', 'ok')).segments)).toBe('Used Linear: Create issue');
    expect(text(stepLine(call('repo_open_pr', 'ok', { payload: { repo: 'acme/web', number: 142 } })).segments)).toBe('Opened pull request #142 in acme/web');
  });

  it('says who decided, with the -ing phrase', () => {
    const a = { id: 'a', status: 'allowed', action: { tool: 'repo_open_pr', payload: { repo: 'acme/web' } } } as unknown as Approval;
    expect(text(decisionLine(a, 'Maya Chen')!.segments)).toBe('Maya Chen approved opening a pull request in acme/web');
    expect(text(decisionLine({ ...a, status: 'denied' }, 'You')!.segments)).toBe('You denied opening a pull request in acme/web');
  });

  it('says the person who asked cancelled their own request', () => {
    const a = {
      id: 'a',
      status: 'denied',
      decided_by: 'usr_alex',
      approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2, requester: 'usr_alex' },
      votes: [{ approval_id: 'a', user_id: 'usr_alex', choice: 'deny', via: 'web', at: '' }],
      action: { tool: 'release_deploy', payload: { repo: 'acme/web', environment: 'production' } },
    } as unknown as Approval;
    expect(text(decisionLine(a, 'Alex Kim')!.segments)).toBe('Cancelled by Alex Kim');
    expect(text(decisionLine(a, 'You')!.segments)).toBe('Cancelled by you');
    expect(text(decisionLine({ ...a, decided_by: 'usr_maya', votes: [{ ...a.votes![0], user_id: 'usr_maya' }] }, 'Maya Chen')!.segments)).toBe('Maya Chen denied deploying acme/web to production');
  });
});

describe('an expired request', () => {
  it('says how many approvals came in, not that nobody answered, when some did', () => {
    const a = {
      id: 'a',
      status: 'expired',
      approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2, requester: 'usr_sam' },
      votes: [{ approval_id: 'a', user_id: 'usr_priya', choice: 'allow_once', via: 'web', at: '' }],
      action: { tool: 'release_deploy', payload: { repo: 'acme/web', environment: 'production' } },
    } as unknown as Approval;
    expect(text(decisionLine(a, '')!.segments)).toBe("Only 1 of 2 approvals came in, so Bothy didn't deploy acme/web to production");
    expect(text(decisionLine({ ...a, votes: [] }, '')!.segments)).toBe("Nobody answered, so Bothy didn't deploy acme/web to production");
    // Stopped while it waited: the chat says so, and so does the task.
    expect(text(decisionLine({ ...a, status: 'cancelled' }, '')!.segments)).toBe("Cancelled: Bothy didn't deploy acme/web to production");
  });
});

describe('audit lines (docs/design/ui.md §10.6)', () => {
  const names = new Map([['usr_01jalex', 'Alex Kim']]);
  const row = (action: string, extra: Partial<AuditEntry> = {}) => ({ id: 1, at: '', actor: 'system', action, target: '', outcome: 'ok', detail: {}, ...extra }) as AuditEntry;

  it('names actors, never with ids', () => {
    expect(actorName('user:usr_01jalex', names)).toBe('Alex Kim');
    expect(actorName('user:usr_01junknown', names)).toBe('Someone');
    expect(actorName('agent:run_01jx', names)).toBe('Bothy');
    expect(actorName('channel:telegram:5512', names)).toBe('Someone on Telegram');
    expect(actorName('token:tok_1', names)).toBe('An API token');
    expect(actorName('agent:run_01jx', names, 'Schedule')).toBe('Schedule');
  });

  it('prefers the server summary and builds a plain one otherwise', () => {
    expect(summarize({ ...row('approval.decide'), summary: 'Approved deleting notes/staging.md' } as AuditEntry)).toBe('Approved deleting notes/staging.md');
    expect(summarize(row('auth.login'))).toBe('Signed in');
    expect(summarize(row('tool.exec', { target: 'web_search' }))).toBe('Searched the web');
    expect(summarize(row('approval.decide', { detail: { choice: 'deny' } }))).toBe('Denied a request');
    expect(summarize(row('egress.deny', { target: 'paste.example.net' }))).toBe('Blocked from sending to paste.example.net');
    expect(summarize(row('picture.update'))).toBe("Changed Bothy's picture");
    expect(summarize(row('picture.reset'))).toBe("Put back Bothy's default picture");
    expect(summarize(row('something.new'))).toBe('Recorded an event');
  });

  it('shows a result word only when something did not succeed', () => {
    expect(resultWord(row('auth.login'))).toBe('');
    // "Blocked from sending to …" already says it.
    expect(summarize(row('egress.deny', { target: 'paste.example.net' }))).toMatch(/^Blocked /);
    expect(resultWord(row('egress.deny', { target: 'paste.example.net' }))).toBe('');
    expect(resultWord(row('tool.exec', { outcome: 'denied', target: 'shell' }))).toBe('Blocked');
    // Reaching the spending limit needs attention but isn't a failure, as in Tasks.
    expect(resultWord(row('budget.exceeded', { outcome: 'error' }))).toBe('');
    expect(resultWord(row('tool.exec', { outcome: 'error', target: 'shell' }))).toBe('Failed');
  });

  it('merges the decision and the result of a connection', () => {
    const e = (decision: string, bytes_down: number) => ({ host: 'api.example.com', decision, bytes_down }) as EgressRecord;
    expect(webLine(e('allow', 4000))).toBe('Reached api.example.com');
    expect(webLine(e('allow', 0))).toBe("Allowed, but api.example.com didn't answer");
    expect(webLine(e('deny', 0))).toBe("Blocked: api.example.com isn't on the allowed list");
  });
});

describe('schedules and tasks in words', () => {
  it('names a schedule from the first words of its prompt', () => {
    expect(nameFrom("Summarise yesterday's merged pull requests in acme/web and post them to #eng.")).toBe("Summarise yesterday's merged pull requests");
    expect(nameFrom('Check status.example.com every morning.')).toBe('Check status.example.com every morning');
  });

  it('says how often a check-in runs and when a schedule runs next', () => {
    expect(every('30m')).toBe('Every 30 min');
    expect(every('1h0m0s')).toBe('Every hour');
    expect(every('15m')).toBe('Every 15 min');
    const now = Date.parse('2026-10-14T10:00:00Z');
    expect(nextRun('2026-10-15T08:00:00Z', 'Europe/London', now, 'en-GB')).toBe('tomorrow at 9:00');
    expect(nextRun('2026-10-14T16:00:00Z', 'Europe/London', now, 'en-GB')).toBe('today at 17:00');
    expect(nextRun('2026-10-17T08:00:00Z', 'Europe/London', now, 'en-GB')).toBe('Saturday at 9:00');
  });

  it('says results stay here: accounts have no chat app', () => {
    expect(deliverTo(undefined)).toBe('here');
    expect(deliverTo({ channel: 'web', chat_id: '', kind: 'dm' })).toBe('here');
    expect(grantSentence({ kind: 'domain', value: 'api.example.com:443' })).toBe('Read websites on api.example.com');
  });

  it('reads a chat permission back as a sentence without naming the chat', () => {
    expect(grantSentence({ kind: 'recipient', value: 'telegram:5512:' })).toBe('Send messages to a chat on Telegram');
    expect(grantSentence({ kind: 'recipient', value: 'discord:99:' })).toBe('Send messages to a chat on Discord');
    expect(grantSentence({ kind: 'recipient', value: 'ops@example.com' })).toBe('Send messages to ops@example.com');
  });

  it('says what a tool permission allows without naming the tool', () => {
    expect(grantSentence({ kind: 'tool', tool: 'fs_write', pattern: { '/path': 'notes/*' } })).toBe('Change files matching notes/*');
    expect(grantSentence({ kind: 'tool', tool: 'mcp__linear__create_issue' })).toBe('Use create issue in Linear');
    expect(grantSentence({ kind: 'tool', tool: 'shell' })).toBe('Run commands');
  });

  it('lists dates without a comma inside each one', () => {
    const t = '2026-10-15T08:00:00Z';
    expect(listDate(t, 'Europe/London', 'en-US')).toBe('Thu Oct 15');
    expect(listDate(t, 'Europe/London', 'en-GB')).toBe('Thu 15 Oct');
    expect(listDate(t, 'Not/AZone', 'en-GB')).toBe('Thu 15 Oct');
  });

  it("adds a task's outcome to its page only when it didn't finish normally", () => {
    const run = (status: string, reason?: string) => ({ status, steps: 3, reason }) as never;
    expect(detailOutcome(run('done'))).toBeNull();
    expect(detailOutcome(run('waiting_approval'))).toBeNull();
    expect(detailOutcome(run('failed', 'the GitHub token was rejected'))).toEqual({ text: 'Failed: the GitHub token was rejected', tone: 'danger' });
    expect(detailOutcome(run('stopped'))).toEqual({ text: 'Stopped', tone: 'muted' });
  });

  it('never folds away just a line or two of code', () => {
    const lines = (n: number) => Array.from({ length: n }, (_, i) => `line ${i}`).join('\n');
    expect(clampLines(lines(9), 8)).toBeUndefined();
    expect(clampLines(lines(10), 8)).toBeUndefined();
    expect(clampLines(lines(11), 8)).toBe(8);
  });

  it('titles a task by its chat or schedule and never shows an id', () => {
    const l: TaskLookups = { names: new Map(), sessions: new Map(), schedules: new Map(), waiting: new Map() };
    const run = { id: 'run_01jx', session_id: 'ses_01jx', user_id: 'usr_01jother', trigger: 'message', status: 'done', steps: 2 } as never;
    const v = taskView(run, l);
    expect(v.title).toBe('A chat');
    expect(v.lead).toEqual(['Someone', 'Web']);
    expect(JSON.stringify(v)).not.toMatch(/usr_|ses_|run_/);
    expect(taskView({ ...(run as object), trigger: 'schedule', schedule_name: 'Daily standup summary' } as never, l)).toMatchObject({ title: 'Daily standup summary', lead: ['Schedule'] });
    // A run whose schedule was deleted keeps no name of its own.
    expect(taskView({ ...(run as object), trigger: 'schedule', schedule_id: undefined, schedule_name: undefined } as never, l)).toMatchObject({ title: 'Deleted schedule' });
  });
});

describe('check-in list', () => {
  it('shows a list of items as plain lines and writes them back as items', () => {
    const file = '- [ ] Tell me if a deploy failed\n- [x] Watch the support inbox\n\n- Check the error rate\n';
    expect(checkinText(file)).toEqual({ text: 'Tell me if a deploy failed\nWatch the support inbox\nCheck the error rate', items: true });
    expect(checkinFile('Tell me if a deploy failed\n\n  Watch the support inbox  ', true)).toBe('- [ ] Tell me if a deploy failed\n- [ ] Watch the support inbox\n');
    expect(checkinFile('', true)).toBe('');
  });

  it('leaves a file that is not only items as it is, so nothing gets mangled', () => {
    const file = '# Morning\n- [ ] Check deploys\nAlso look at the queue';
    expect(checkinText(file)).toEqual({ text: file, items: false });
    expect(checkinFile(file, false)).toBe(file);
  });
});
