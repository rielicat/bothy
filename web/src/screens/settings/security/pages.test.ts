import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { egress, grants, ids, member } from '../../../../mock/fixtures';
import { resetMock } from '../../../../mock/server';
import { toaster } from '../../../components/ui';
import { config } from '../../../lib/config.svelte';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';
import Permissions from './Permissions.svelte';
import SavedKeys from './SavedKeys.svelte';
import UnlockScreen from './UnlockScreen.svelte';
import Websites from './Websites.svelte';

async function signIn(mock = '', override?: (m: string, url: string) => unknown): Promise<Call[]> {
  const calls = installApi({ override });
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  config.view = null;
  return calls;
}

beforeEach(() => toaster.dismiss());

describe('Allowed without asking', () => {
  it('lists permissions in plain words with who they are for', async () => {
    await signIn();
    render(Permissions);
    await screen.findByText('Send messages to a chat in Slack');
    expect(screen.getByText('www.edinburghvenues.example')).toBeTruthy();
    expect(screen.getByText('pytest*')).toBeTruthy();
    expect(screen.getAllByText(/^For Sam ·/).length).toBeGreaterThan(0);
    expect(screen.getByText(/^For Alex Kim · Slack · added by Sam · Slack ·/)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/grt_|usr_|shell/);
  });

  it('names a pinned request in words and keeps its pattern behind Details', async () => {
    const pinned = { ...grants[2], id: 'grt_pinned', tool: 'mcp__linear__create_issue', pattern: { '': '{"title":"Fix login"}' } };
    await signIn('', (m, url) => (m === 'GET' && url === '/api/grants' ? [pinned] : undefined));
    render(Permissions);
    await screen.findByText('Use create issue in Linear with these exact details');
    expect(document.body.textContent).not.toMatch(/Fix login|mcp__/);
    await fireEvent.click(screen.getByRole('button', { name: 'Details' }));
    expect(document.body.textContent).toMatch(/mcp__linear__create_issue/);
  });

  it('removes one, keeps focus in the list and confirms', async () => {
    const calls = await signIn();
    render(Permissions);
    await screen.findByText('Send messages to a chat in Slack');
    const before = screen.getAllByRole('button', { name: 'Remove' });
    await fireEvent.click(before[0]);
    await waitFor(() => expect(screen.getAllByRole('button', { name: 'Remove' })).toHaveLength(before.length - 1));
    expect(calls.some((c) => c.method === 'DELETE' && c.url.startsWith('/api/grants/'))).toBe(true);
    await waitFor(() => expect(document.activeElement).toBe(screen.getAllByRole('button', { name: 'Remove' })[0]));
    expect(toaster.current?.text).toBe('Permission removed.');
  });

  it('says what the page is for when it is empty', async () => {
    await signIn('security-empty');
    render(Permissions);
    expect(await screen.findByText('Nothing is allowed without asking yet.')).toBeTruthy();
  });
});

describe('Websites Bothy can reach', () => {
  it('lists the sites and what was blocked recently, by chat name', async () => {
    const inChat = { ...egress.find((e) => e.decision === 'deny')!, session_id: ids.trip };
    await signIn('', (_m, url) => (url.startsWith('/api/egress') ? { items: [inChat] } : undefined));
    render(Websites);
    await screen.findByText('api.github.com');
    expect(screen.getByText('*.wikipedia.org')).toBeTruthy();
    const blocked = screen.getByRole('heading', { name: 'Blocked recently' }).closest('section')!;
    await waitFor(() => expect(within(blocked).getByText(inChat.host)).toBeTruthy());
    expect(within(blocked).getByText(/From “Edinburgh team offsite”/)).toBeTruthy();
  });

  it('adds what someone types as a clean site name', async () => {
    const calls = await signIn();
    render(Websites);
    await fireEvent.click(await screen.findByRole('button', { name: 'Add website' }));
    const dialog = screen.getByRole('dialog', { name: 'Add website' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add website' }));
    expect(within(dialog).getByText('Enter a website address, like docs.example.com.')).toBeTruthy();
    expect(document.activeElement).toBe(within(dialog).getByLabelText('Website address'));
    await fireEvent.input(within(dialog).getByLabelText('Website address'), { target: { value: 'api.github.com' } });
    expect(within(dialog).getByText('Bothy can already reach api.github.com.')).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Website address'), { target: { value: 'https://Docs.Acme.com/handbook' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add website' }));
    await screen.findByText('docs.acme.com');
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set['egress.allow']).toContain('docs.acme.com');
    await waitFor(() => expect(toaster.current?.text).toBe('Website added.'));
  });

  it('allows a blocked site after asking, and it moves to the list', async () => {
    const calls = await signIn();
    render(Websites);
    const allow = await screen.findByRole('button', { name: 'Allow this site…' });
    await fireEvent.click(allow);
    const dialog = screen.getByRole('dialog', { name: 'Allow paste.example.net?' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Allow site' }));
    await waitFor(() => expect(screen.getByText('Nothing was blocked recently.')).toBeTruthy());
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set['egress.allow']).toContain('paste.example.net');
  });

  it('removes a site', async () => {
    const calls = await signIn();
    render(Websites);
    await fireEvent.click(await screen.findByRole('button', { name: 'Remove docs.python.org' }));
    await waitFor(() => expect(screen.queryByText('docs.python.org')).toBeNull());
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set['egress.allow']).not.toContain('docs.python.org');
  });
});

describe('Saved keys', () => {
  it('lists keys by friendly name, what uses them and when, never the value', async () => {
    await signIn();
    render(SavedKeys);
    await screen.findByText('Anthropic API key');
    const meta = [...document.querySelectorAll('p.text-small')].map((p) => p.textContent ?? '');
    const line = (re: RegExp) => meta.find((t) => re.test(t.replace(/\s+/g, ' ')));
    expect(line(/^Used by Anthropic · last used \d+ min ago$/)).toBeTruthy();
    expect(line(/^Used by OpenRouter · not used yet$/)).toBeTruthy();
    expect(line(/^Used by Brave Search · set on the server$/)).toBeTruthy();
    // On phones the line breaks between pieces, never before a "·" or inside "last used 2 h ago".
    expect(line(/^Used by Anthropic/)).toContain('Anthropic\u00a0· ');
    expect(document.querySelector('p.text-small time')?.closest('.whitespace-nowrap')?.textContent).toMatch(/^last used /);
    // A key from the server environment can't be replaced or deleted here.
    expect(screen.queryByRole('button', { name: 'Replace Brave Search API key' })).toBeNull();
    expect(document.body.textContent).not.toMatch(/x9Qa|anthropic_api_key/);
  });

  it('adds a key under a stored name, checking the fields first', async () => {
    const calls = await signIn();
    render(SavedKeys);
    await fireEvent.click(await screen.findByRole('button', { name: 'Add key' }));
    const dialog = screen.getByRole('dialog', { name: 'Add key' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save key' }));
    expect(within(dialog).getByText('Enter a name for the key.')).toBeTruthy();
    expect(within(dialog).getByText('Paste the key.')).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Tavily API key' } });
    await fireEvent.input(within(dialog).getByLabelText('Value'), { target: { value: 'tvly-secret-1234' } });
    expect(within(dialog).getByText('Stored encrypted. Only sent to the service that uses it.')).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save key' }));
    await screen.findByText('Tavily API key');
    const put = calls.find((c) => c.method === 'PUT')!;
    expect(put.url).toBe('/api/vault/global/tavily_api_key');
    expect(JSON.parse(put.body)).toEqual({ value: 'tvly-secret-1234' });
    await waitFor(() => expect(toaster.current?.text).toBe('Key saved.'));
  });

  it('replaces a key, naming where it is sent', async () => {
    const calls = await signIn();
    render(SavedKeys);
    await fireEvent.click(await screen.findByRole('button', { name: 'Replace Anthropic API key' }));
    const dialog = screen.getByRole('dialog', { name: 'Replace Anthropic API key' });
    expect(within(dialog).getByText('Stored encrypted. Only sent to Anthropic.')).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('New value'), { target: { value: 'sk-ant-new' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save key' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.url === '/api/vault/global/anthropic_api_key')).toBe(true));
    await waitFor(() => expect(toaster.current?.text).toBe('Key replaced.'));
  });

  it('deletes a key after a confirmation that starts on Cancel', async () => {
    const calls = await signIn();
    render(SavedKeys);
    await fireEvent.click(await screen.findByRole('button', { name: 'More for Anthropic API key' }));
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete Anthropic API key?' });
    expect(within(dialog).getByText("Anthropic will stop working until you add a new key. This can't be undone.")).toBeTruthy();
    expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Cancel' }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete key' }));
    await waitFor(() => expect(screen.queryByText('Anthropic API key')).toBeNull());
    expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/vault/global/anthropic_api_key')).toBe(true);
  });

  it('leaves Unlock to the locked-keys banner while it shows', async () => {
    await signIn('sealed');
    render(SavedKeys);
    await screen.findByRole('heading', { level: 1 });
    await waitFor(() => expect(session.banners.some((b) => b.code === 'vault_sealed')).toBe(true));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText('Saved keys are locked.')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Unlock' })).toBeNull();
  });

  it('unlocks locked keys with the passphrase', async () => {
    const calls = await signIn('sealed');
    // Without the banner (dismissed or not yet here) the page offers Unlock itself.
    session.banners = session.banners.filter((b) => b.code !== 'vault_sealed');
    render(SavedKeys);
    await screen.findByText('Saved keys are locked.');
    expect(screen.queryByRole('button', { name: 'Add key' })).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    const dialog = screen.getByRole('dialog', { name: 'Unlock saved keys' });
    await fireEvent.input(within(dialog).getByLabelText('Passphrase'), { target: { value: 'wrong' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Unlock' }));
    expect(await within(dialog).findByText("That passphrase didn't work. Check it and try again.")).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Passphrase'), { target: { value: 'right horse battery' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Unlock' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Saved keys unlocked.'));
    expect(calls.filter((c) => c.url === '/api/vault/unseal')).toHaveLength(2);
    await waitFor(() => expect(screen.queryByText('Saved keys are locked.')).toBeNull());
    expect(screen.getByRole('button', { name: 'Add key' })).toBeTruthy();
  });
});

describe('Your saved keys', () => {
  it("lets a member add and delete their own keys, and lists no one else's", async () => {
    const calls = await signIn('member');
    render(SavedKeys, { params: { group: 'account', item: 'keys' } });
    expect(await screen.findByText('No saved keys yet.')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'Your saved keys' })).toBeTruthy();
    expect(screen.getByText('Keys only you use. When a connected tool works for you, it uses your key in place of the shared key with the same name.')).toBeTruthy();
    expect(screen.queryByText('Anthropic API key')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Add key' }));
    const dialog = screen.getByRole('dialog', { name: 'Add key' });
    expect(within(dialog).getByText('Use the name of the shared key the connected tool uses. An admin can tell you what it is.')).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'mcp_linear_header' } });
    await fireEvent.input(within(dialog).getByLabelText('Value'), { target: { value: 'Bearer lin-own-1234' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save key' }));
    await screen.findByText('Key for Linear');
    const put = calls.find((c) => c.method === 'PUT')!;
    expect(put.url).toBe(`/api/vault/${encodeURIComponent(`user:${member.id}`)}/mcp_linear_header`);
    // Admin-only reads would only fail for a member.
    expect(calls.some((c) => c.url.startsWith('/api/vault/status') || c.url.startsWith('/api/config'))).toBe(false);

    await fireEvent.click(screen.getByRole('button', { name: 'More for Key for Linear' }));
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    const del = await screen.findByRole('dialog', { name: 'Delete Key for Linear?' });
    expect(within(del).getByText("This can't be undone.")).toBeTruthy();
    await fireEvent.click(within(del).getByRole('button', { name: 'Delete key' }));
    await waitFor(() => expect(screen.queryByText('Key for Linear')).toBeNull());
    expect(calls.some((c) => c.method === 'DELETE' && c.url === put.url)).toBe(true);
  });

  it('still shows a member the workspace keys page as admins only', async () => {
    await signIn('member');
    render(SavedKeys);
    expect(await screen.findByText('Only admins can see this page.')).toBeTruthy();
  });
});

describe('Saved keys limits', () => {
  it('says a name or a value is too long before sending it', async () => {
    const calls = await signIn();
    render(SavedKeys);
    await fireEvent.click(await screen.findByRole('button', { name: 'Add key' }));
    const dialog = await screen.findByRole('dialog');
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Linear API key for the engineering team workspace production east' } });
    await fireEvent.input(within(dialog).getByLabelText(/^(Key|Value)/), { target: { value: 'x'.repeat(70_000) } });
    await fireEvent.click(within(dialog).getByRole('button', { name: /Save/ }));
    expect(await within(dialog).findByText('Use a shorter name, up to 64 characters.')).toBeTruthy();
    expect(within(dialog).getByText('This is too long for a key: keys can be up to 64 KB.')).toBeTruthy();
    expect(calls.filter((c) => c.method === 'PUT')).toEqual([]);
  });
});

describe('Unlock screen', () => {
  it('says why no passphrase can unlock the keys, instead of "Try again"', async () => {
    const why = "A passphrase can't unlock these saved keys: they are locked because Bothy's key file is missing or doesn't match. Put the key file Bothy was set up with back on the server, then restart Bothy.";
    await signIn('sealed', (m, url) => (m === 'POST' && url === '/api/vault/unseal' ? new Response(JSON.stringify({ error: { code: 'bad_request', message: why } }), { status: 400 }) : undefined));
    render(UnlockScreen);
    await fireEvent.input(screen.getByLabelText('Passphrase'), { target: { value: 'right horse battery' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText(why)).toBeTruthy();
    expect(screen.queryByText("Couldn't unlock the saved keys. Try again.")).toBeNull();
  });

  it('unlocks from the blocking screen and hands back to the shell', async () => {
    await signIn('sealed');
    const onunlocked = vi.fn();
    render(UnlockScreen, { onunlocked });
    expect(screen.getByRole('heading', { level: 1, name: 'Unlock saved keys' })).toBeTruthy();
    expect(screen.getByText("Bothy can't reach its models or connections until you enter the passphrase.")).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText('Enter the passphrase.')).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText('Passphrase'));
    await fireEvent.input(screen.getByLabelText('Passphrase'), { target: { value: 'right horse battery' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    await waitFor(() => expect(onunlocked).toHaveBeenCalledOnce());
  });
});
