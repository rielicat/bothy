import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it } from 'vitest';
import { member, owner } from '../../../../mock/fixtures';
import { session } from '../../../lib/stores/session.svelte';
import { FakeEventSource } from '../../../test/setup';
import { installApi, type Call } from '../../../test/api';
import ChatApp from './ChatApp.svelte';

const SLACK_APP = 'xapp-' + '1-A0123456789-1234567890123-abcdef';
const SLACK_BOT = 'xoxb-' + '1234567890-1234567890123-AbCdEfGhIjKlMnOp';
const refuse = (status = 500, message = 'could not save') => new Response(JSON.stringify({ error: { code: 'internal', message } }), { status });

// Slack as on a fresh install: turned off, no tokens saved.
const fresh = (more?: (m: string, url: string) => unknown) => (m: string, url: string) =>
  more?.(m, url) ??
  (url === '/api/vault' ? [] : url === '/api/channels' ? [{ name: 'slack', enabled: false, state: 'disabled', since: '', deep_links: false }] : undefined);

async function setup(item: string, override?: (m: string, url: string) => unknown): Promise<Call[]> {
  const calls = installApi({ override });
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

describe('Connect Slack (§12.3)', () => {
  it('shows the numbered steps with the plan copy', async () => {
    await setup('slack', fresh());
    expect(await screen.findByRole('heading', { name: /^Step 1: ?Create the Slack app$/ })).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'Connect Slack' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: /^Step 2: ?Paste the two tokens$/ })).toBeTruthy();
    expect(screen.getByRole('heading', { name: /^Step 3: ?Link your Slack account$/ })).toBeTruthy();
    expect(screen.getByText('Starts with xapp-. In Slack: Basic Information → App-Level Tokens.')).toBeTruthy();
    expect(screen.getByText('You can do this once step 2 is done.')).toBeTruthy();
  });

  it('writes nothing until both tokens look right, then saves, turns it on and checks', async () => {
    const calls = await setup('slack', fresh());
    await fireEvent.input(await screen.findByLabelText('App-level token'), { target: { value: SLACK_APP } });
    await fireEvent.keyDown(screen.getByLabelText('App-level token'), { key: 'Enter' });
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);

    await pasteTokens();
    expect(await screen.findByText(/Connected as @bothy\./)).toBeTruthy();
    const writes = calls.filter((c) => c.method !== 'GET').map((c) => `${c.method} ${c.url}`);
    expect(writes).toEqual(['PUT /api/vault/global/slack_app_token', 'PUT /api/vault/global/slack_bot_token', 'PATCH /api/config', 'POST /api/channels/slack/test', 'POST /api/pairing']);
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set).toEqual({
      'channels.slack.enabled': true,
      'channels.slack.app_token': 'secret:slack_app_token',
      'channels.slack.bot_token': 'secret:slack_bot_token',
    });
    // Step 3 then offers the link command for Slack's slash command.
    expect(await screen.findByText('/bothy pair 7KQ2-MX4D')).toBeTruthy();
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

  it('shows "Linked as" when the person sends the code', async () => {
    await setup('slack', fresh());
    await pasteTokens();
    await screen.findByText('/bothy pair 7KQ2-MX4D');
    FakeEventSource.last?.emit('pairing.linked', { type: 'pairing.linked', data: { channel: 'slack', external_id: 'U1', display: '@maya' } });
    await waitFor(() => expect(screen.queryByText('Linked as @maya.')).toBeTruthy());
  });
});

describe('Slack manage view', () => {
  it('lists group chats, linked people and who waits', async () => {
    await setup('slack');
    expect(await screen.findByRole('heading', { level: 1, name: 'Slack' })).toBeTruthy();
    expect(screen.getByText('Connected to Acme as @bothy')).toBeTruthy();
    expect(screen.getByText('#eng')).toBeTruthy();
    expect(screen.getByText('Sam · @sam')).toBeTruthy();
    expect(screen.getByText('Alex · @alex')).toBeTruthy();
    expect(screen.getByText('Jordan Lee · @jordan')).toBeTruthy();
  });

  // Linking hands the chat account that person's chats and permissions, so
  // the request must name exactly the person the owner picked.
  it('links a sender only to the person the owner chose', async () => {
    const calls = await setup('slack');
    await fireEvent.click(await screen.findByRole('button', { name: 'Options for @jordan' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Link to a person…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Link @jordan to a person' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Link account' }));
    expect(await within(dialog).findByText('Choose a person.')).toBeTruthy();
    expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))).toBe(false);

    await fireEvent.change(within(dialog).getByLabelText('Person'), { target: { value: member.id } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Link account' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))!.body)).toEqual({ status: 'linked', user_id: member.id });
  });

  // The gateway lets only the owner link a chat account to someone else.
  it('offers an admin no one to link to but themselves', async () => {
    installApi();
    await session.init();
    session.me = { ...session.me!, user: { ...session.me!.user, role: 'admin' } };
    render(ChatApp, { params: { group: 'connections', item: 'slack' } });
    await fireEvent.click(await screen.findByRole('button', { name: 'Options for @jordan' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Link to a person…' }));
    const select = within(await screen.findByRole('dialog')).getByLabelText('Person') as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toEqual([owner.id]);
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
    await fireEvent.input(await screen.findByLabelText('Bot token'), { target: { value: 'MTIzNDU2Nzg5MDEyMzQ1Njc4' + '.GhIjKl.MnOpQrStUvWxYzAbCdEfGhIjKlMnOpQrStUvWx' } });
    const id = screen.getByLabelText('Application ID');
    await fireEvent.input(id, { target: { value: '112233445566778899' } });
    await tick();
    await fireEvent.keyDown(id, { key: 'Enter' });
    expect((await screen.findByRole('alert')).textContent).toContain('Discord needs Message Content Intent turned on for this bot.');
  });

  it('offers the QR code and Open Telegram once connected', async () => {
    await setup('telegram', (m, url) =>
      url === '/api/vault' ? [] : url === '/api/channels' ? [{ name: 'telegram', enabled: false, state: 'disabled', since: '', deep_links: true }] : undefined,
    );
    const token = await screen.findByLabelText('Bot token');
    await fireEvent.input(token, { target: { value: '123456789:' + 'AAH4kq9xV1t8mZp2Lq0wE3rTyUiOpAsDfGh' } });
    await tick();
    await fireEvent.keyDown(token, { key: 'Enter' });
    expect(await screen.findByText('Connected as @acme_bothy_bot.')).toBeTruthy();
    const open = await screen.findByRole('link', { name: /Open Telegram/ });
    expect(open.getAttribute('href')).toBe('https://t.me/acme_bothy_bot?start=7KQ2MX4D');
    expect(open.getAttribute('target')).toBe('_blank');
  });
});
