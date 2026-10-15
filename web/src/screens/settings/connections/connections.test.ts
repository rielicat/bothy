import { describe, expect, it } from 'vitest';
import { ApiError } from '../../../lib/api/client';
import type { MCPStatus } from '../../../lib/api/types.gen';
import {
  approverShortfall,
  appStatus,
  approverLine,
  channelFailure,
  envConfig,
  envErrors,
  failureCode,
  githubOutcome,
  githubStatus,
  needsAttention,
  readAt,
  searchFailure,
  serverTitle,
  toolStatus,
  type AppStatus,
  type EnvForm,
} from './connections';
import type { GitHubConnection, GitHubProbe } from './types';

const app = (over: Partial<AppStatus>): AppStatus => ({ name: 'slack', enabled: true, state: 'connected', since: '', deep_links: false, ...over });
const tool = (over: Partial<MCPStatus>): MCPStatus => ({ name: 'linear', transport: 'http', state: 'ready', protocol: '', tools: 12, pending: 0, since: '', ...over });

describe('chat app status words (§12.1)', () => {
  it('reads "Not connected" until tokens exist, and "Off" when turned off with tokens', () => {
    expect(appStatus('discord', undefined, 0, false)).toEqual({ text: 'Not connected', tone: 'muted', action: 'Connect' });
    expect(appStatus('discord', app({ enabled: false, state: 'disabled' }), 0, false).text).toBe('Not connected');
    expect(appStatus('slack', app({ enabled: false, state: 'disabled' }), 2, true)).toEqual({ text: 'Off', tone: 'muted', action: 'Manage' });
  });

  it('names the workspace or the bot, and counts linked people', () => {
    expect(appStatus('slack', app({ workspace: 'Acme', identity: '@bothy' }), 4, true).text).toBe('Connected to Acme · 4 people linked');
    expect(appStatus('telegram', app({ identity: '@acme_bot' }), 1, true).text).toBe('Connected as @acme_bot · 1 person linked');
    expect(appStatus('telegram', app({ identity: '@acme_bot' }), 0, true).text).toBe('Connected as @acme_bot');
  });

  it('says why it cannot connect, in red, with Reconnect', () => {
    expect(appStatus('telegram', app({ state: 'auth_failed' }), 0, true)).toEqual({ text: "Can't connect: Telegram didn't accept the token", tone: 'danger', action: 'Reconnect' });
    expect(appStatus('slack', app({ state: 'backoff' }), 0, true).text).toBe("Can't connect: Slack isn't answering");
    expect(appStatus('slack', app({ state: 'connecting' }), 0, true).text).toBe('Connecting…');
  });
});

describe('tool and GitHub status words', () => {
  it('covers every MCP state without the word MCP', () => {
    expect(toolStatus(tool({}))).toEqual({ text: 'Ready · 12 actions', tone: 'muted', action: 'Manage' });
    expect(toolStatus(tool({ state: 'pins_pending', pending: 2 }))).toEqual({ text: 'Needs review: 2 actions', tone: 'warning', action: 'Review' });
    expect(toolStatus(tool({ state: 'installing' })).text).toBe('Installing…');
    expect(toolStatus(tool({ state: 'disabled' })).text).toBe('Off');
    expect(toolStatus(tool({ state: 'error' }))).toEqual({ text: "Can't connect: the server didn't answer", tone: 'danger', action: 'Reconnect' });
    expect(toolStatus(tool({ state: 'no_sandbox', transport: 'stdio' }))).toEqual({ text: "Can't connect: code can't run on this server", tone: 'danger', action: 'Manage' });
    expect(toolStatus(undefined).text).toBe('Connecting…');
  });

  it('names the GitHub account and how many repositories Bothy may use', () => {
    const c = { account: 'acme-bot', auth: 'pat', status: 'ok' } as GitHubConnection;
    expect(githubStatus(undefined, 0).text).toBe('Not connected');
    expect(githubStatus(c, 6).text).toBe('Connected as acme-bot · 6 repositories');
    expect(githubStatus(c, 1).text).toBe('Connected as acme-bot · 1 repository');
    expect(githubStatus({ ...c, status: 'error' }, 6).tone).toBe('danger');
  });

  it('asks for attention only when something is broken or waits for review', () => {
    const ok = app({});
    expect(needsAttention([ok], [tool({})], [])).toBe(false);
    expect(needsAttention([app({ enabled: false, state: 'disabled' })], [], [])).toBe(false);
    expect(needsAttention([app({ state: 'auth_failed' })], [], [])).toBe(true);
    expect(needsAttention([ok], [tool({ state: 'pins_pending', pending: 1 })], [])).toBe(true);
    expect(needsAttention([ok], [], [{ status: 'error' } as GitHubConnection])).toBe(true);
  });

  it('titles servers and lists approvers in plain words', () => {
    expect(serverTitle('linear')).toBe('Linear');
    expect(serverTitle('team-docs')).toBe('Team docs');
    expect(serverTitle('deepwiki')).toBe('DeepWiki');
    expect(serverTitle('github')).toBe('GitHub');
    const approvers = [
      { kind: 'user', id: 'usr_1', name: 'Alex Kim' },
      { kind: 'role', id: 'admin', name: 'admin' },
      { kind: 'user', id: 'usr_2', name: 'Priya Rao' },
    ];
    expect(approverLine({ required: 2, approvers })).toBe('2 approvals from Alex Kim, Priya Rao, any admin');
    expect(approverLine({ required: 0, approvers: [approvers[1]] })).toBe('1 approval from any admin');
    expect(approverLine({ required: 1, approvers: [{ kind: 'user', id: 'usr_gone', name: '' }] })).toBe('1 approval from a removed person');
  });

  it('says when a repository was last read', () => {
    const now = new Date(2026, 9, 14, 15, 0).getTime();
    expect(readAt(new Date(2026, 9, 14, 9, 12).toISOString(), now)).toMatch(/^Read today at \d{1,2}[.:]12/);
    expect(readAt(new Date(2026, 9, 13, 14, 2).toISOString(), now)).toMatch(/^Read yesterday at /);
    expect(readAt(new Date(2026, 9, 14, 9, 12).toISOString(), now)).not.toMatch(/at 09:12 [AP]M/);
    expect(readAt(new Date(2026, 9, 2).toISOString(), now)).toMatch(/^Read (2 Oct|Oct 2)/);
    expect(readAt('0001-01-01T00:00:00Z', now)).toBe('');
  });
});

describe('failures (§5.6.4, §12)', () => {
  it('prefers the server code, then status and known phrases', () => {
    expect(failureCode(new ApiError(502, 'rate_limited', 'x'))).toBe('rate_limited');
    expect(failureCode(new Error('discord: gateway closed with code 4014: Disallowed intent(s)'))).toBe('intent');
    expect(failureCode(new Error('slack: auth.test: invalid_auth'))).toBe('key_rejected');
    expect(failureCode(new Error('dial tcp: lookup api.github.com: no such host'))).toBe('unreachable');
    expect(failureCode(new Error('x509: certificate signed by unknown authority'))).toBe('tls');
    expect(failureCode(new ApiError(429, 'too_many', 'slow down'))).toBe('rate_limited');
    expect(failureCode(new Error('boom'))).toBe('unknown');
  });

  it('gives each chat app its own sentence, with the raw error kept for Details', () => {
    expect(channelFailure('slack', new Error('slack: auth.test: invalid_auth'))).toEqual({
      message: "Slack didn't accept those tokens. Check you copied the app-level token and the bot token from the same app.",
      details: 'slack: auth.test: invalid_auth',
    });
    expect(channelFailure('telegram', new Error('401 Unauthorized')).message).toBe("Telegram didn't accept that token. Copy it again from @BotFather.");
    expect(channelFailure('discord', new Error('close 4014')).message).toBe(
      'Discord needs Message Content Intent turned on for this bot. Turn it on in the developer portal under Bot, then try again.',
    );
    expect(channelFailure('slack', new Error('dial tcp: i/o timeout')).message).toBe(
      "Bothy couldn't reach Slack from this server. Check the server's internet connection or firewall.",
    );
  });

  it('turns a GitHub check into the §12.2 sentences', () => {
    const p = (over: Partial<GitHubProbe>): GitHubProbe => ({ ok: true, auth: 'pat', account: 'maya-acme', account_type: 'User', repos: [], truncated: false, token_expires_at: '', rate_limit: { limit: 0, remaining: 0, reset: '' }, ...over });
    const repo = { github_id: 1, owner: 'acme', name: 'web', private: true, archived: false, default_branch: 'main', allowed: false };
    expect(githubOutcome(p({ repos: Array(14).fill(repo) }), null, false)).toEqual({ ok: true, message: 'Connected as maya-acme. Bothy can see 14 repositories.' });
    expect(githubOutcome(p({ auth: 'app', account: 'acme', repos: [repo] }), null, true)).toEqual({ ok: true, message: 'Installed on acme · 1 repository' });
    expect(githubOutcome(p({}), null, false)).toMatchObject({ ok: false, message: "The token works but can't see any repositories. Give it access to at least one repository on GitHub." });
    expect(githubOutcome(p({ ok: false, error: 'GitHub rejected the credentials (wrong, expired or revoked token)' }), null, false)).toMatchObject({
      message: "GitHub didn't accept that token. It may have expired or been revoked.",
    });
    expect(githubOutcome(p({ ok: false, error: 'dial tcp: no such host' }), null, false)).toMatchObject({ message: "Bothy couldn't reach GitHub. Check the address and that this server can reach it." });
    expect(githubOutcome(p({ ok: false, error: 'the App is not installed on any account yet' }), null, true)).toMatchObject({ notInstalled: true, message: "The app isn't installed on any organisation yet." });
  });

  it('words web search failures', () => {
    expect(searchFailure('searxng', 'private_address', 'raw')).toEqual({ message: 'Bothy blocks addresses on your private network unless you allow them.', details: 'raw', allow: true });
    expect(searchFailure('searxng', 'unreachable', '').message).toBe("SearXNG isn't answering at that address. Make sure it's running.");
    expect(searchFailure('brave', 'out_of_credit', '').message).toBe('Your Brave Search account is out of credit. Add credit with Brave Search, then try again.');
    expect(searchFailure('tavily', 'nonsense', '').message).toBe('Something went wrong while checking. Try again, or choose another option.');
  });
});

describe('deploy environments', () => {
  const form = (over: Partial<EnvForm>): EnvForm => ({ name: 'production', repo: 'acme/web', workflow: 'deploy.yml', branches: 'main', approvers: ['admin'], required: 1, inputs: [], ...over });

  it('checks the name, the workflow, the branches and who can approve', () => {
    expect(envErrors(form({}), [])).toEqual({});
    expect(envErrors(form({ name: 'Prod!' }), []).name).toBe('Use lowercase letters, numbers, - and _, like production.');
    expect(envErrors(form({}), ['production']).name).toBe('There is already an environment with this name.');
    expect(envErrors(form({ workflow: ' ', branches: '' }), [])).toMatchObject({ workflow: 'Choose the workflow Bothy starts.', branches: 'Enter at least one branch, like main.' });
    expect(envErrors(form({ approvers: [] }), []).approvers).toBe('Choose who can approve.');
  });

  // Two approvals need two people besides the requester, as the server checks.
  it('wants two people, or a role, for two approvals', () => {
    expect(envErrors(form({ required: 2, approvers: ['usr_01a'] }), []).approvers).toBe('Choose at least two people, or Any admin.');
    expect(envErrors(form({ required: 2, approvers: ['usr_01a', 'usr_01b'] }), []).approvers).toBeUndefined();
    expect(envErrors(form({ required: 2, approvers: ['usr_01a', 'admin'] }), []).approvers).toBeUndefined();
  });

  it('writes the config shape the server reads', () => {
    expect(envConfig(form({ branches: ' main ,  release/* ', inputs: [{ key: 'version', value: '1.2' }, { key: ' ', value: 'x' }] }))).toEqual({
      name: 'production',
      repo: 'acme/web',
      workflow: 'deploy.yml',
      ref_pattern: 'main,release/*',
      approvers: ['admin'],
      required: 1,
      inputs: { version: '1.2' },
    });
  });
});

describe('approverShortfall', () => {
  const u = (id: string, role: string, name: string, disabled = false) => ({ id, role, disabled, display_name: name, username: name.toLowerCase() });
  const team = [u('usr_o', 'owner', 'Tess'), u('usr_p', 'admin', 'Priya'), u('usr_m', 'member', 'Max'), u('usr_x', 'admin', 'Gone', true)];

  it('passes when enough other people can approve', () => {
    expect(approverShortfall(['admin'], 1, team)).toBe('');
    expect(approverShortfall(['member'], 2, team)).toBe('');
  });

  it('warns when an approver asking could never get enough others', () => {
    expect(approverShortfall(['admin'], 2, team)).toBe("Only 2 people can approve. A deploy one of them asks for can't get 2 approvals from the others. Add another approver or ask for 1.");
    expect(approverShortfall(['usr_p'], 1, team)).toBe("Only Priya can approve, so a deploy Priya asks for can't be approved. Add another approver.");
    expect(approverShortfall(['usr_p'], 2, team)).toBe('Only 1 person can approve, so a deploy could never get 2 approvals. Add approvers or ask for fewer.');
    expect(approverShortfall([], 1, team)).toBe('Nobody can approve these deploys yet. Choose who can approve.');
  });
});
