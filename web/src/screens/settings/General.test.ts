import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { toaster } from '../../components/ui';
import { setCsrf } from '../../lib/api/client';
import { config } from '../../lib/config.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import Settings from './Settings.svelte';
import { settingsConfig } from './store.svelte';

const patches = (calls: Call[]) => calls.filter((c) => c.method === 'PATCH' && c.url === '/api/config').map((c) => JSON.parse(c.body).set);

/** The settings row a label belongs to. */
const row = (label: string) => screen.getByText(label, { selector: 'label' }).closest('.grid') as HTMLElement;

describe('Settings → General', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    config.view = null;
    render(Settings, { params: { group: 'general', page: 'general' } });
    await screen.findByText('Workspace');
  });

  it('shares its config with the pages that read the schema, so it is fetched once', async () => {
    await config.load();
    expect(calls.filter((c) => c.method === 'GET' && c.url.startsWith('/api/config'))).toHaveLength(1);
  });

  it('says plain HTTP from another computer is a risk, with the advice in view', async () => {
    session.me!.access = { peer: 'lan', tls: false };
    const out = await screen.findByText('Reached over plain HTTP from another computer');
    const row = out.closest('.grid')!;
    expect(row.textContent).toContain('Put Bothy behind HTTPS, for example with Tailscale or Caddy, before your team uses it from other computers.');
    // No table repeating the headline, and no raw "(lan)".
    expect(row.textContent).not.toMatch(/Reached from|\(lan\)/);
    expect(within(row as HTMLElement).queryByRole('button', { name: 'Details' })).toBeNull();
  });

  it('saves a text field on blur and says "Saved" in its live region', async () => {
    const limit = screen.getByLabelText('Daily limit') as HTMLInputElement;
    expect(limit.value).toBe('3.00');
    await fireEvent.input(limit, { target: { value: '7.5' } });
    expect(patches(calls)).toEqual([]);
    await fireEvent.blur(limit);
    await waitFor(() => expect(patches(calls)).toEqual([{ 'budgets.daily_usd': 7.5 }]));
    const live = await within(row('Daily limit')).findByText('Saved');
    expect(live.getAttribute('aria-live')).toBe('polite');
    await waitFor(() => expect(limit.value).toBe('7.50'));
  });

  it('saves on Enter, and not at all when nothing changed', async () => {
    const name = screen.getByLabelText('Name') as HTMLInputElement;
    await fireEvent.keyDown(name, { key: 'Enter' });
    await fireEvent.blur(name);
    expect(patches(calls)).toEqual([]);
    await fireEvent.input(name, { target: { value: 'Ada' } });
    await fireEvent.keyDown(name, { key: 'Enter' });
    await waitFor(() => expect(patches(calls)).toEqual([{ 'agent.name': 'Ada' }]));
  });

  it("checks Bothy's name the way the server does", async () => {
    const name = screen.getByLabelText('Name') as HTMLInputElement;
    for (const [value, error] of [
      ['   ', 'Enter a name.'],
      ['x'.repeat(41), 'Use at most 40 characters.'],
      ['<b>Ada</b>', 'Leave out < and >.'],
    ]) {
      await fireEvent.input(name, { target: { value } });
      await fireEvent.keyDown(name, { key: 'Enter' });
      expect(await screen.findByText(error)).toBeTruthy();
    }
    expect(patches(calls)).toEqual([]);
  });

  it('offers the tone as three cards with a sample reply, and saves the one picked', async () => {
    const group = screen.getByRole('radiogroup', { name: 'Tone' });
    const radios = within(group).getAllByRole('radio') as HTMLInputElement[];
    expect(radios.map((r) => r.closest('label')!.querySelector('span')!.textContent)).toEqual(['Warm and brief', 'Straight to the point', 'Calm and formal']);
    expect(group.textContent).toContain('“Good morning. The release notes are ready for your review.”');
    expect(radios.find((r) => r.checked)?.value).toBe('warm');
    await fireEvent.click(within(group).getByLabelText(/Calm and formal/));
    await waitFor(() => expect(patches(calls)).toEqual([{ 'agent.tone': 'formal' }]));
    expect((within(group).getByLabelText(/Calm and formal/) as HTMLInputElement).checked).toBe(true);
  });

  it('puts the tone back when saving it fails', async () => {
    installApi({ override: (m, url) => (m === 'PATCH' && url === '/api/config' ? new Response(JSON.stringify({ error: { code: 'internal', message: 'x' } }), { status: 500 }) : undefined) });
    const group = screen.getByRole('radiogroup', { name: 'Tone' });
    await fireEvent.click(within(group).getByLabelText(/Straight to the point/));
    expect(await screen.findByRole('alert')).toBeTruthy();
    await waitFor(() => expect((within(group).getByLabelText(/Warm and brief/) as HTMLInputElement).checked).toBe(true));
  });

  it("shows today's spending against the limit above the limit", async () => {
    const meter = await screen.findByRole('progressbar', { name: "Today's spending" });
    expect(meter.getAttribute('aria-valuetext')).toMatch(/^\$\d+\.\d\d of \$\d+\.\d\d$/);
    expect(screen.getByText('Resets at midnight')).toBeTruthy();
  });

  it('keeps an invalid value unsaved and says why under the field', async () => {
    const web = screen.getByLabelText('Web address') as HTMLInputElement;
    await fireEvent.input(web, { target: { value: 'bothy.acme' } });
    await fireEvent.keyDown(web, { key: 'Enter' });
    expect(await screen.findByText('Use a full address, like https://bothy.acme.com.')).toBeTruthy();
    expect(web.getAttribute('aria-invalid')).toBe('true');
    expect(patches(calls)).toEqual([]);
    await fireEvent.input(web, { target: { value: 'https://bothy.acme.com/' } });
    await fireEvent.blur(web);
    await waitFor(() => expect(patches(calls)).toEqual([{ 'server.public_url': 'https://bothy.acme.com' }]));
  });

  it('says which settings take effect only after a restart', () => {
    // server.public_url is restart-only in the fixture config; the daily limit applies at once.
    expect(within(row('Web address')).getByText(/Takes effect after a restart\./)).toBeTruthy();
    expect(within(row('Daily limit')).queryByText(/Takes effect after a restart\./)).toBeNull();
  });

  it("says the workspace time zone is new people's default and the daily limit's clock, not everyone's schedules", () => {
    expect(screen.getByText('New people start with this time zone. The daily spending limit resets at midnight here.')).toBeTruthy();
    expect(screen.queryByText(/Used for schedules/)).toBeNull();
  });

  it('keeps the standing instructions as one list item per line', async () => {
    const box = screen.getByLabelText('Standing instructions');
    await fireEvent.input(box, { target: { value: 'Reply in British English.\n\n  Link to the code.  ' } });
    await fireEvent.blur(box);
    await waitFor(() => expect(patches(calls)).toEqual([{ 'agent.standing_rules': ['Reply in British English.', 'Link to the code.'] }]));
  });

  it("shows a conflict instead of overwriting another admin's change", async () => {
    const name = screen.getByLabelText('Name') as HTMLInputElement;
    await fireEvent.input(name, { target: { value: 'Ada' } });
    // Someone else saves the same setting while this field is being edited.
    await settingsConfig.save({ 'agent.name': 'Grace' });
    expect(await screen.findByText(/Someone else changed this setting\. Reload to see their change\./)).toBeTruthy();
    const before = patches(calls).length;
    await fireEvent.blur(name);
    expect(patches(calls).length).toBe(before);
    await fireEvent.click(screen.getByRole('button', { name: 'Reload' }));
    expect(name.value).toBe('Grace');
    expect(screen.queryByText(/Someone else changed this setting/)).toBeNull();
  });
});

describe('Settings → General, who sees it', () => {
  it('tells a member that only admins can see it', async () => {
    installApi();
    session.ready = false;
    await session.init();
    session.me = { ...session.me!, user: { ...session.me!.user, role: 'member' } };
    render(Settings, { params: { group: 'general', page: 'general' } });
    expect(await screen.findByText('Only admins can see this page.')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'General' })).toBeTruthy();
  });

  it('says a settings page that does not exist does not exist', async () => {
    installApi();
    session.ready = false;
    await session.init();
    render(Settings, { params: { group: 'nope', page: 'nope' } });
    expect(await screen.findByText('Check the address, or go back to your chats.')).toBeTruthy();
  });
});

describe("Settings → General, Bothy's picture", () => {
  async function open(opts: { mock?: string; override?: (m: string, url: string) => unknown } = {}): Promise<Call[]> {
    const calls = installApi({ override: opts.override });
    if (opts.mock) resetMock({ mock: opts.mock });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    config.view = null;
    toaster.dismiss();
    render(Settings, { params: { group: 'general', page: 'general' } });
    await screen.findByRole('button', { name: 'Change…' });
    return calls;
  }

  const picker = () => document.querySelector<HTMLInputElement>('input[type="file"]')!;
  const choose = (file: File) => fireEvent.change(picker(), { target: { files: [file] } });
  const png = () => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'logo.png', { type: 'image/png' });
  const picture = () => row('Picture');
  /** The text of what aria-describedby points at. */
  const description = (el: Element) =>
    (el.getAttribute('aria-describedby') ?? '')
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent)
      .join(' ');

  it('shows the picture and, with Slack on, how to put it in Slack', async () => {
    await open();
    const r = picture();
    expect(within(r).getByRole('img', { name: 'Bothy' }).getAttribute('src')).toMatch(/^\/api\/agent\/picture\?v=/);
    expect(within(r).getByText("Shown on Bothy's replies here and in the chat apps.")).toBeTruthy();
    expect(within(r).queryByRole('button', { name: 'Use default' })).toBeNull();
    expect(within(r).getByText("In Slack, upload it under App icon in Slack's app settings.")).toBeTruthy();
    const download = within(r).getByRole('link', { name: 'Download picture' });
    expect(download.getAttribute('href')).toBe('/api/agent/picture?download=1');
    expect(download.hasAttribute('download')).toBe(true);
    const slack = within(r).getByRole('link', { name: /^Open Slack's app settings/ });
    expect(slack.getAttribute('href')).toBe('https://api.slack.com/apps/A0123456789/general');
    expect(slack.getAttribute('target')).toBe('_blank');
    expect(r.textContent).not.toMatch(/avatar|\bPNG\b|\bAPI\b|bot token/i);
  });

  it('leaves Slack out when it is off, and links to the list of apps until Slack names the app', async () => {
    await open({ override: (_m, url) => (url === '/api/channels' ? [{ name: 'slack', enabled: false, state: 'disabled', since: '' }] : undefined) });
    expect(within(picture()).queryByText(/In Slack/)).toBeNull();
    cleanup();
    await open({ override: (_m, url) => (url === '/api/channels' ? [{ name: 'slack', enabled: true, state: 'connecting', since: '' }] : undefined) });
    expect(within(picture()).getByRole('link', { name: /^Open Slack's app settings/ }).getAttribute('href')).toBe('https://api.slack.com/apps');
  });

  it('uploads a new picture, says so and offers the default again', async () => {
    const calls = await open();
    const before = within(picture()).getByRole('img').getAttribute('src');
    await choose(png());
    await waitFor(() => expect(toaster.current?.text).toBe('Picture changed.'));
    const put = calls.find((c) => c.method === 'PUT' && c.url === '/api/agent/picture')!;
    expect(put.headers['x-csrf-token']).toBe('mock-csrf');
    expect(put.body).toContain('filename=');
    const r = picture();
    expect(within(r).getByRole('img').getAttribute('src')).not.toBe(before);
    expect(within(r).getByRole('button', { name: 'Use default' })).toBeTruthy();
    // Telegram took it; Slack still needs a person, which its line already says.
    expect(r.textContent).not.toMatch(/Updated in Telegram|Try again/);
    const slack = within(r).getAllByText("In Slack, upload it under App icon in Slack's app settings.");
    expect(slack).toHaveLength(1);
    expect(slack[0].closest('p')!.className).toContain('text-warning-text');
  });

  it('says in one line per chat app what could not be changed', async () => {
    await open({ mock: 'discord-on,picture-fail' });
    await choose(png());
    await waitFor(() => expect(toaster.current?.text).toBe('Picture changed.'));
    const r = picture();
    expect(await within(r).findByText('Discord only allows a few picture changes an hour. Try again later.')).toBeTruthy();
    expect(within(r).getByText("Couldn't change the picture in Telegram. Try again later.")).toBeTruthy();
    expect(within(r).queryByText(/Updated in/)).toBeNull();
  });

  it('keeps the old picture and says why when the file is refused', async () => {
    const calls = await open({ mock: 'picture-refused' });
    await choose(png());
    expect(await within(picture()).findByText("That file isn't a picture Bothy can use. Choose another one.")).toBeTruthy();
    expect(toaster.current).toBeNull();
    expect(within(picture()).queryByRole('button', { name: 'Use default' })).toBeNull();

    // Over the server's cap, nothing is sent.
    const sent = calls.filter((c) => c.method === 'PUT').length;
    await choose(new File([new Uint8Array(5 * 1024 * 1024 + 1)], 'huge.jpg', { type: 'image/jpeg' }));
    expect(await within(picture()).findByText('That picture is over 5 MB. Choose a smaller one.')).toBeTruthy();
    expect(calls.filter((c) => c.method === 'PUT').length).toBe(sent);
  });

  it('goes back to the default picture', async () => {
    const calls = await open({ mock: 'picture-custom' });
    const ghost = within(picture()).getByRole('button', { name: 'Use default' });
    ghost.focus();
    await fireEvent.click(ghost);
    await waitFor(() => expect(toaster.current?.text).toBe('Default picture restored.'));
    expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/agent/picture')).toBe(true);
    // With Telegram on the button stays, to send the default again, and keeps focus.
    expect(await within(picture()).findByRole('button', { name: 'Send to chat apps' })).toBe(document.activeElement);
  });

  it('moves focus to Change… when going back to the default leaves no button', async () => {
    await open({ mock: 'picture-custom', override: (m, url) => (m === 'GET' && url === '/api/channels' ? [{ name: 'slack', enabled: true, state: 'connected' }] : undefined) });
    await fireEvent.click(within(picture()).getByRole('button', { name: 'Use default' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Default picture restored.'));
    await waitFor(() => expect(within(picture()).queryByRole('button', { name: 'Use default' })).toBeNull());
    expect(document.activeElement).toBe(within(picture()).getByRole('button', { name: 'Change…' }));
  });

  it('offers to go back to the default again when a chat app missed it', async () => {
    let deletes = 0;
    const calls = await open({
      mock: 'picture-custom,picture-fail',
      override: (m, url) =>
        m === 'DELETE' && url === '/api/agent/picture' && ++deletes === 3 ? { picture: { custom: false, version: 'v9' }, apps: [{ app: 'telegram', result: 'updated', detail: 'Updated in Telegram.' }] } : undefined,
    });
    await fireEvent.click(within(picture()).getByRole('button', { name: 'Use default' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Default picture restored.'));
    const r = picture();
    expect(within(r).getByText("Couldn't change the picture in Telegram. Try again later.")).toBeTruthy();
    expect(within(r).queryByRole('button', { name: 'Use default' })).toBeNull();
    await fireEvent.click(within(r).getByRole('button', { name: 'Try again' }));
    await waitFor(() => expect(deletes).toBe(2));
    expect(await within(picture()).findByRole('button', { name: 'Try again' })).toBeTruthy();
    expect(within(picture()).getByText("Couldn't change the picture in Telegram. Try again later.")).toBeTruthy();
    await fireEvent.click(within(picture()).getByRole('button', { name: 'Try again' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Picture sent to the chat apps.'));
    expect(within(picture()).queryByRole('button', { name: 'Try again' })).toBeNull();
    expect(within(picture()).queryByText(/Try again later/)).toBeNull();
    expect(within(picture()).getByRole('button', { name: 'Send to chat apps' })).toBeTruthy();
    expect(calls.filter((c) => c.method === 'DELETE').length).toBe(3);
  });

  it('sends the default to Telegram and Discord again, for a bot with an older picture', async () => {
    const calls = await open();
    await fireEvent.click(within(picture()).getByRole('button', { name: 'Send to chat apps' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Picture sent to the chat apps.'));
    expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/agent/picture')).toBe(true);
    expect(await within(picture()).findByRole('button', { name: 'Send to chat apps' })).toBeTruthy();
  });

  it('claims no sending when no chat app took the picture', async () => {
    await open({ mock: 'picture-fail' });
    await fireEvent.click(within(picture()).getByRole('button', { name: 'Send to chat apps' }));
    expect(await within(picture()).findByText("Couldn't change the picture in Telegram. Try again later.")).toBeTruthy();
    expect(toaster.current).toBeNull();
  });

  it('offers no sending when only Slack is on', async () => {
    await open({ override: (m, url) => (m === 'GET' && url === '/api/channels' ? [{ name: 'slack', enabled: true, state: 'connected' }] : undefined) });
    expect(within(picture()).queryByRole('button', { name: 'Send to chat apps' })).toBeNull();
  });

  it('names the row on its buttons for screen readers', async () => {
    await open({ mock: 'picture-custom' });
    for (const name of ['Change…', 'Use default']) {
      expect(description(within(picture()).getByRole('button', { name }))).toBe("Picture Shown on Bothy's replies here and in the chat apps.");
    }
  });

  it('says when the picture did not load, and tries again', async () => {
    let fail = true;
    installApi({ override: (_m, url) => (url === '/api/agent/picture/info' && fail ? new Response(JSON.stringify({ error: { code: 'internal', message: 'x' } }), { status: 500 }) : undefined) });
    session.ready = false;
    await session.init();
    config.view = null;
    render(Settings, { params: { group: 'general', page: 'general' } });
    const line = await screen.findByText("The picture didn't load. Check your connection and try again.");
    expect(within(picture()).queryByRole('button', { name: 'Change…' })).toBeNull();
    fail = false;
    await fireEvent.click(within(line.closest('.grid') as HTMLElement).getByRole('button', { name: 'Try again' }));
    // The row is drawn anew once the picture is there.
    await waitFor(() => expect(document.activeElement).toBe(within(picture()).getByRole('button', { name: 'Change…' })));
  });

  it('leaves the row out when the server has no picture to show', async () => {
    installApi({ override: (_m, url) => (url === '/api/agent/picture/info' ? new Response(JSON.stringify({ error: { code: 'unavailable', message: 'x' } }), { status: 503 }) : undefined) });
    session.ready = false;
    await session.init();
    config.view = null;
    render(Settings, { params: { group: 'general', page: 'general' } });
    await screen.findByText('Personality');
    expect(screen.queryByText('Picture', { selector: 'label' })).toBeNull();
  });
});
