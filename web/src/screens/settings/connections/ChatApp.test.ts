import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../../mock/server';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';
import ChatApp from './ChatApp.svelte';

const SLACK_APP = 'xapp-' + '1-A0123456789-1234567890123-abcdef';
const SLACK_BOT = 'xoxb-' + '1234567890-1234567890123-AbCdEfGhIjKlMnOp';
const refuse = (status = 500, message = 'could not save') => new Response(JSON.stringify({ error: { code: 'internal', message } }), { status });

// Slack as on a fresh install: turned off, no tokens saved.
const fresh = (more?: (m: string, url: string) => unknown) => (m: string, url: string) =>
  more?.(m, url) ?? (url === '/api/vault' ? [] : url === '/api/channels' ? [{ name: 'slack', enabled: false, state: 'disabled', since: '' }] : undefined);

async function setup(item: string, override?: (m: string, url: string) => unknown, mock = ''): Promise<Call[]> {
  const calls = installApi({ override });
  if (mock) resetMock({ mock });
  await session.init();
  render(ChatApp, { params: { group: 'connections', item } });
  return calls;
}

async function pasteTokens() {
  await fireEvent.input(await screen.findByLabelText('App-level token'), { target: { value: SLACK_APP } });
  const bot = screen.getByLabelText('Bot token');
  await fireEvent.input(bot, { target: { value: SLACK_BOT } });
  await tick();
  await fireEvent.keyDown(bot, { key: 'Enter' });
}

const who = async () => within((await screen.findByRole('heading', { name: 'Who can use Bothy' })).closest('section')!);

describe('Connect Slack (§12.3)', () => {
  it('shows the numbered steps with the plan copy and no linking step', async () => {
    await setup('slack', fresh());
    expect(await screen.findByRole('heading', { name: /^Step 1: ?Create the Slack app$/ })).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'Connect Slack' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: /^Step 2: ?Add Bothy's picture ?\(optional\)$/ })).toBeTruthy();
    expect(screen.getByRole('heading', { name: /^Step 3: ?Paste the two tokens$/ })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: /^Step 4/ })).toBeNull();
    expect(screen.getByText('Starts with xapp-. In Slack: Basic Information → App-Level Tokens → Generate Token and Scopes. Add the connections:write scope.')).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/link|pair/i);
  });

  it('writes nothing until both tokens look right, then saves, turns it on, checks and says how to try it', async () => {
    const calls = await setup('slack', fresh());
    await fireEvent.input(await screen.findByLabelText('App-level token'), { target: { value: SLACK_APP } });
    await fireEvent.keyDown(screen.getByLabelText('App-level token'), { key: 'Enter' });
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);

    await pasteTokens();
    expect(await screen.findByText(/Connected as @bothy\./)).toBeTruthy();
    const writes = calls.filter((c) => c.method !== 'GET').map((c) => `${c.method} ${c.url}`);
    expect(writes).toEqual(['PUT /api/vault/global/slack_bot_token', 'PUT /api/vault/global/slack_app_token', 'PATCH /api/config', 'POST /api/channels/slack/test']);
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set).toEqual({
      'channels.slack.enabled': true,
      'channels.slack.app_token': 'secret:slack_app_token',
      'channels.slack.bot_token': 'secret:slack_bot_token',
    });
    const tryIt = await screen.findByText(/^Message Bothy in Slack to try it\./);
    expect(tryIt.textContent).toBe('Message Bothy in Slack to try it. To approve requests there, turn on Can approve for yourself in People.');
    expect(within(tryIt).getByRole('link', { name: 'People' }).getAttribute('href')).toBe('/settings/people');
  });

  it("offers Bothy's picture for the app icon without making it a step to finish", async () => {
    const calls = await setup('slack', fresh());
    const step = (await screen.findByRole('heading', { name: /Add Bothy's picture/ })).closest('li')!;
    expect(within(step).getByText('In Slack, open Basic Information and upload it under App icon.')).toBeTruthy();
    expect(within(step).getByRole('img', { name: 'Bothy' }).getAttribute('src')).toBe('/api/agent/picture');
    const download = within(step).getByRole('link', { name: 'Download picture' });
    expect(download.getAttribute('href')).toBe('/api/agent/picture?download=1');
    expect(download.hasAttribute('download')).toBe(true);
    expect(step.textContent).not.toMatch(/avatar|\bPNG\b|\bAPI\b/i);
    await pasteTokens();
    expect(await screen.findByText(/Connected as @bothy\./)).toBeTruthy();
    expect(calls.some((c) => c.url.startsWith('/api/agent/picture'))).toBe(false);
  });

  it('says what went wrong in Slack terms, with the raw error behind Details', async () => {
    await setup(
      'slack',
      fresh((m, url) => (m === 'POST' && url === '/api/channels/slack/test' ? refuse(502, 'slack: auth.test: invalid_auth') : undefined)),
    );
    await pasteTokens();
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain("Slack didn't accept those tokens. Check you copied the app-level token and the bot token from the same app.");
    expect(screen.queryByText('slack: auth.test: invalid_auth')).toBeNull();
  });

  it('keeps neither the refused tokens nor Slack switched on after a failed first connection', async () => {
    const calls = await setup(
      'slack',
      fresh((m, url) => (m === 'POST' && url === '/api/channels/slack/test' ? refuse(502, 'slack: auth.test: invalid_auth') : undefined)),
    );
    await pasteTokens();
    await screen.findByRole('alert');
    await waitFor(() => expect(calls.filter((c) => c.method === 'DELETE' && c.url.startsWith('/api/vault/global/')).length).toBe(2));
    const patches = calls.filter((c) => c.method === 'PATCH' && c.url === '/api/config').map((c) => JSON.parse(c.body).set);
    expect(patches.at(-1)).toEqual({ 'channels.slack.enabled': false });
  });
});

describe('Slack manage view', () => {
  it('lists group chats with who is in them, and says who can use Bothy', async () => {
    await setup('slack');
    expect(await screen.findByRole('heading', { level: 1, name: 'Slack' })).toBeTruthy();
    expect(screen.getByText('Connected to Acme as @bothy')).toBeTruthy();
    expect(screen.getByText('#eng')).toBeTruthy();
    expect(screen.getByText('Acme · 24 people')).toBeTruthy();
    expect(screen.getByText('Acme · 57 people · Shared with other companies')).toBeTruthy();
    const s = await who();
    expect(s.getByText(/^Everyone in Acme can message Bothy and use what it can read/).textContent?.replace(/\s+/g, ' ').trim()).toBe(
      "Everyone in Acme can message Bothy and use what it can read, such as your repositories and connected tools. Guests and people from other companies can't. Anything that changes something waits for an approver.",
    );
    // Slack decides who is in the team, so the select for new people is not here.
    expect(s.queryByLabelText('When someone new messages Bothy')).toBeNull();
    expect(s.getByRole('link', { name: 'Manage people' }).getAttribute('href')).toBe('/settings/people');
    expect(document.body.textContent).not.toMatch(/Linked people|aren't linked|as a guest/);
  });

  it("hands over Bothy's picture for Slack's app icon, with a link to the app's settings", async () => {
    await setup('slack');
    const r = (await screen.findByText('Picture', { selector: 'label' })).closest('.grid') as HTMLElement;
    expect(within(r).getByText("In Slack, upload it under App icon in Slack's app settings.")).toBeTruthy();
    const download = within(r).getByRole('link', { name: 'Download picture' });
    expect(download.getAttribute('href')).toBe('/api/agent/picture?download=1');
    expect(document.getElementById(download.getAttribute('aria-describedby')!)?.textContent).toBe('Picture');
    expect(within(r).getByRole('link', { name: /^Open Slack's app settings/ }).getAttribute('href')).toBe('https://api.slack.com/apps/A0123456789/general');
    expect(within(r).getAllByRole('link')).toHaveLength(2);
  });

  it('says an org-wide install lets in the whole organization', async () => {
    await setup('slack', (_m, url) => (url === '/api/channels' ? [{ name: 'slack', enabled: true, state: 'connected', since: '', identity: '@bothy', team: 'Acme', org: true }] : undefined));
    const s = await who();
    expect(s.getByText(/^Everyone in your Slack organization, across all its workspaces, can message Bothy/)).toBeTruthy();
  });

  it('chooses what Bothy can do in a group chat in plain words', async () => {
    const calls = await setup('slack');
    const what = (await screen.findAllByLabelText('What Bothy can do here'))[0] as HTMLSelectElement;
    expect([...what.options].map((o) => o.text)).toEqual(['Answer questions', 'Do work, with approval']);
    expect(what.value).toBe('standard');
    await fireEvent.change(what, { target: { value: 'restricted' } });
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/spaces/spc_01jeng00000000000000000000' && JSON.parse(c.body).tool_profile === 'restricted')).toBe(true));
  });

  it('puts a group chat control back when the change fails', async () => {
    const calls = await setup('slack', (m, url) => (m === 'PATCH' && url.startsWith('/api/spaces/') ? refuse() : undefined));
    const when = (await screen.findAllByLabelText('Answers when'))[0] as HTMLSelectElement;
    expect(when.value).toBe('mention');
    await fireEvent.change(when, { target: { value: 'any' } });
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/spaces/'))).toBe(true));
    await waitFor(() => expect(when.value).toBe('mention'));
    expect(await screen.findByText("Couldn't save. Try again.")).toBeTruthy();
  });

  it('replaces rejected tokens from a failed test, then comes back to manage', async () => {
    let tests = 0;
    const calls = await setup('slack', (m, url) =>
      m === 'POST' && url === '/api/channels/slack/test' && tests++ === 0 ? refuse(502, 'slack: auth.test: invalid_auth') : undefined,
    );
    await fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }));
    await fireEvent.click(await screen.findByRole('button', { name: 'Replace the tokens' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'Connect Slack' })).toBeTruthy();
    expect(screen.getByText(/^Slack didn't accept those tokens\./)).toBeTruthy();
    // The app exists already, so its picture is not asked for again.
    expect(screen.queryByRole('heading', { name: /Add Bothy's picture/ })).toBeNull();
    expect(screen.getByRole('heading', { name: /^Step 2: ?Paste the two tokens$/ })).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Replace the bot token' }));
    const bot = await screen.findByLabelText('Bot token');
    await waitFor(() => expect(document.activeElement).toBe(bot));
    await fireEvent.input(bot, { target: { value: SLACK_BOT } });
    await tick();
    await fireEvent.keyDown(bot, { key: 'Enter' });
    expect(await screen.findByRole('heading', { level: 1, name: 'Slack' })).toBeTruthy();
    const writes = calls.filter((c) => c.method !== 'GET').map((c) => `${c.method} ${c.url}`);
    expect(writes).toEqual(['POST /api/channels/slack/test', 'PUT /api/vault/global/slack_bot_token', 'PATCH /api/config', 'POST /api/channels/slack/test']);
  });

  it('disconnects after a confirmation and forgets the tokens', async () => {
    const calls = await setup('slack');
    await fireEvent.click(await screen.findByRole('button', { name: 'Disconnect Slack…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Disconnect Slack?' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Disconnect Slack' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.url)).toEqual(['/api/vault/global/slack_app_token', '/api/vault/global/slack_bot_token']));
    expect(await screen.findByRole('heading', { level: 1, name: 'Connect Slack' })).toBeTruthy();
  });
});

describe('Discord and Telegram', () => {
  it('explains a missing Message Content Intent', async () => {
    await setup('discord', (m, url) => (m === 'POST' && url === '/api/channels/discord/test' ? refuse(502, 'discord: gateway closed with code 4014: Disallowed intent(s)') : undefined));
    expect(await screen.findByText('Under Bot, turn off Public Bot, so only you can add Bothy to servers.')).toBeTruthy();
    await fireEvent.input(await screen.findByLabelText('Bot token'), { target: { value: 'MTIzNDU2Nzg5MDEyMzQ1Njc4' + '.GhIjKl.MnOpQrStUvWxYzAbCdEfGhIjKlMnOpQrStUvWx' } });
    const id = screen.getByLabelText('Application ID');
    await fireEvent.input(id, { target: { value: '112233445566778899' } });
    await tick();
    await fireEvent.keyDown(id, { key: 'Enter' });
    expect((await screen.findByRole('alert')).textContent).toContain('Discord needs Message Content Intent turned on for this bot.');
  });

  it('offers Open Telegram once connected', async () => {
    await setup('telegram', (m, url) => (url === '/api/vault' ? [] : url === '/api/channels' ? [{ name: 'telegram', enabled: false, state: 'disabled', since: '' }] : undefined));
    const token = await screen.findByLabelText('Bot token');
    await fireEvent.input(token, { target: { value: '123456789:' + 'AAH4kq9xV1t8mZp2Lq0wE3rTyUiOpAsDfGh' } });
    await tick();
    await fireEvent.keyDown(token, { key: 'Enter' });
    expect(await screen.findByText('Connected as @acme_bothy_bot.')).toBeTruthy();
    // Telegram can't say who is in the team, so the admin is let in like anyone else.
    const tryIt = await screen.findByText(/^Message Bothy in Telegram, then/);
    expect(tryIt.textContent).toBe('Message Bothy in Telegram, then let yourself in from Approvals. To approve requests there, turn on Can approve for yourself in People.');
    expect(within(tryIt).getByRole('link', { name: 'Approvals' }).getAttribute('href')).toBe('/approvals');
    const open = await screen.findByRole('link', { name: /Open Telegram/ });
    expect(open.getAttribute('href')).toBe('https://t.me/acme_bothy_bot');
    expect(open.getAttribute('target')).toBe('_blank');
  });

  it('lets Telegram admins choose what happens when someone new messages Bothy', async () => {
    const calls = await setup('telegram');
    const s = await who();
    expect(s.getByText('People you let in from Approvals.')).toBeTruthy();
    const select = s.getByLabelText('When someone new messages Bothy') as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toEqual(['Ask an admin (recommended)', "Don't reply or notify anyone"]);
    expect(s.getByText("Either way, they're listed in Approvals. Applies to Telegram and Discord.")).toBeTruthy();
    await fireEvent.change(select, { target: { value: 'ignore' } });
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/config' && JSON.parse(c.body).set['channels.unknown_senders'] === 'ignore')).toBe(true));
  });

  it('says who can use Bothy on Discord, and asks before the last channel of a server goes off', async () => {
    const calls = await setup('discord', undefined, 'discord-on');
    const s = await who();
    expect(s.getByText(/^Members of a server where Bothy answers in a channel can message Bothy/)).toBeTruthy();
    expect(screen.getByText('Acme Community · 4,812 people')).toBeTruthy();
    const on = screen.getByRole('switch', { name: 'Answers here' });
    await fireEvent.click(on);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && JSON.parse(c.body).enabled === true)).toBe(true));
    await fireEvent.click(on);
    const dialog = await screen.findByRole('dialog', { name: 'Turn off Bothy in #general?' });
    expect(dialog.textContent).toContain("People who are only in Acme Community won't be able to message Bothy any more.");
    expect(calls.some((c) => c.method === 'PATCH' && JSON.parse(c.body).enabled === false)).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(on.getAttribute('aria-checked')).toBe('true'));
    await fireEvent.click(on);
    await fireEvent.click(within(await screen.findByRole('dialog', { name: 'Turn off Bothy in #general?' })).getByRole('button', { name: 'Turn off' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && JSON.parse(c.body).enabled === false)).toBe(true));
  });

  it("says when Discord can't check who is in the servers, and when Bothy sits idle in some", async () => {
    await setup('discord', undefined, 'discord-on:no-access');
    expect((await who()).getByText("Bothy can't check who's in your servers, so only people you let in from Approvals can message it.")).toBeTruthy();
  });

  it('asks to remove Bothy from servers where no channel is on', async () => {
    await setup('discord', undefined, 'discord-on:idle');
    expect((await who()).getByText("Bothy is in 2 servers where it isn't on in any channel. Remove it from servers you don't use.")).toBeTruthy();
  });

  it("reads the status's fields, not its sentence, so both lines can show at once", async () => {
    const status = { name: 'discord', enabled: true, state: 'connected', identity: '@Bothy', since: '', detail: 'Worded some other way.', member_check: 'no_access', idle_servers: 1 };
    await setup('discord', (m, url) => (m === 'GET' && url === '/api/channels' ? [status] : undefined), 'discord-on');
    const s = await who();
    expect(s.getByText("Bothy can't check who's in your servers, so only people you let in from Approvals can message it.")).toBeTruthy();
    expect(s.getByText("Bothy is in 1 server where it isn't on in any channel. Remove it from servers you don't use.")).toBeTruthy();
    expect(document.body.textContent).not.toContain('Worded some other way.');
  });

  it('deletes a group chat only once it is off, after a confirmation', async () => {
    const calls = await setup('slack');
    const del = await screen.findByRole('button', { name: 'Delete #support…' });
    expect(screen.queryByRole('button', { name: 'Delete #eng…' })).toBeNull();
    await fireEvent.click(del);
    const dialog = await screen.findByRole('dialog', { name: 'Delete #support?' });
    expect(dialog.textContent).toContain(
      "This deletes its chats, files, notes and schedules in Bothy, and what Bothy may do there without asking. If Bothy is still in #support, the next message there brings it back to Approvals. This can't be undone.",
    );
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete group chat' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/spaces/spc_01jsupport0000000000000000')).toBe(true));
    await waitFor(() => expect(screen.queryByText('#support')).toBeNull());
  });
});
