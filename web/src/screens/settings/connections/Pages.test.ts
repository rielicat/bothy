import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';
import Connections from './Connections.svelte';
import { connectionsScreen } from './index';
import McpServer from './McpServer.svelte';
import WebSearch from './WebSearch.svelte';

async function start(override?: (m: string, url: string) => unknown, member = false): Promise<Call[]> {
  const calls = installApi({ override });
  await session.init();
  if (member) session.me = { ...session.me!, user: { ...session.me!.user, role: 'member' } };
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');

describe('Connections overview (§12.1)', () => {
  it('lists chat apps, GitHub and tools with one button each and no MCP jargon in rows', async () => {
    await start();
    render(Connections);
    expect(await screen.findByText('Connected to Acme · 2 people linked')).toBeTruthy();
    expect(screen.getByText('Connected as @acme_bothy_bot · 1 person linked')).toBeTruthy();
    expect(screen.getByText('Not connected')).toBeTruthy();
    expect(screen.getByText('Connected as acme-bot · 2 repositories')).toBeTruthy();
    expect(screen.getByText('Needs review: 1 action')).toBeTruthy();
    expect(screen.getByText('Ready · 12 actions')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Review Docs' }).getAttribute('href')).toBe('/settings/connections/mcp-docs?review=1');
    expect(screen.getByRole('link', { name: 'Connect Discord' }).getAttribute('href')).toBe('/settings/connections/discord');
    expect(screen.getByRole('link', { name: 'Change Web search' }).getAttribute('href')).toBe('/settings/connections/search');
    expect([...new Set(screen.queryAllByText(/MCP/).map((e) => e.textContent))]).toEqual(['Add an MCP server']);
  });

  it('shows members that only admins can see it', async () => {
    await start(undefined, true);
    render(Connections);
    expect(await screen.findByText('Only admins can see this page.')).toBeTruthy();
    expect(screen.queryByText('Chat apps')).toBeNull();
  });

  it('adds a server by its web address and keeps the secret header in saved keys', async () => {
    const calls = await start();
    render(Connections);
    await fireEvent.click(await screen.findByRole('button', { name: 'Add an MCP server' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add an MCP server' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add server' }));
    expect(within(dialog).getByText('Give the server a name.')).toBeTruthy();
    expect(writes(calls)).toEqual([]);

    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'sentry' } });
    await fireEvent.input(within(dialog).getByLabelText('Address'), { target: { value: 'https://mcp.sentry.dev/mcp' } });
    await fireEvent.input(within(dialog).getByLabelText(/Secret header/), { target: { value: 'Bearer abc123' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add server' }));
    await waitFor(() => expect(writes(calls).map((c) => `${c.method} ${c.url}`)).toEqual(['PUT /api/vault/global/mcp_sentry_header', 'PATCH /api/config']));
    expect(JSON.parse(writes(calls)[1].body).set).toEqual({
      'mcp.servers.sentry': { name: 'sentry', transport: 'http', side_effects: { default: 'external_send' }, url: 'https://mcp.sentry.dev/mcp', headers: { Authorization: 'secret:mcp_sentry_header' } },
    });
  });

  it('keeps web search first and ends Tools with the add button when no servers exist', async () => {
    await start((m, url) => (m === 'GET' && url === '/api/mcp' ? [] : undefined));
    render(Connections);
    const empty = await screen.findByText('No tools connected.');
    const search = screen.getByText('Web search');
    expect(search.compareDocumentPosition(empty) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getAllByRole('button', { name: 'Add an MCP server' })).toHaveLength(1);
  });

  it('moves focus to the field that needs fixing when adding a server', async () => {
    const calls = await start();
    render(Connections);
    await fireEvent.click(await screen.findByRole('button', { name: 'Add an MCP server' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add an MCP server' });
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'sentry' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add server' }));
    await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByLabelText('Address')));
    expect(writes(calls)).toEqual([]);
  });

  it('says why a command server is not an option when code cannot run', async () => {
    await start((m, url) => (url === '/api/sandbox' ? { status: { available: false } } : undefined));
    render(Connections);
    await fireEvent.click(await screen.findByRole('button', { name: 'Add an MCP server' }));
    await fireEvent.click(await screen.findByRole('radio', { name: 'A command on this server' }));
    expect(
      await screen.findByText("This server runs as a command, and code can't run on this server. Use its web address instead, or turn on running code in Security."),
    ).toBeTruthy();
    expect(screen.queryByLabelText('Package')).toBeNull();
  });

  it('routes every Connections address to its page', () => {
    expect(connectionsScreen()).toBe(Connections);
    expect(connectionsScreen('mcp-linear')).toBe(McpServer);
    expect(connectionsScreen('search')).toBe(WebSearch);
    expect(connectionsScreen('slack')).not.toBeNull();
    expect(connectionsScreen('github')).not.toBeNull();
    expect(connectionsScreen('nope')).toBeNull();
    expect(connectionsScreen('mcp-')).toBeNull();
  });
});

describe('MCP server page (§12.4)', () => {
  it('lists actions in plain words and changes what one does with the reviewed hash', async () => {
    const calls = await start();
    render(McpServer, { params: { item: 'mcp-linear' } });
    expect(await screen.findByText('Ready · 12 actions')).toBeTruthy();
    expect(screen.getByText('Create issue')).toBeTruthy();
    const selects = screen.getAllByLabelText(/^What .* does$/) as HTMLSelectElement[];
    expect([...selects[0].options].map((o) => o.textContent)).toEqual(['Only reads information', 'Changes things inside Bothy', 'Sends data out', "Can't be undone"]);
    await fireEvent.change(selects[0], { target: { value: 'external_send' } });
    await waitFor(() => expect(writes(calls).length).toBe(1));
    expect(JSON.parse(writes(calls)[0].body)).toEqual({ tools: { list_issues: { side_effect: 'external_send', private: false, hash: 'a1' } } });
  });

  it('reviews changed actions in one dialog and saves every decision at once', async () => {
    const calls = await start();
    render(McpServer, { params: { item: 'mcp-docs' } });
    expect(await screen.findByText('1 action changed and needs a review before Bothy can use it.')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));
    const dialog = await screen.findByRole('dialog', { name: 'Review changes to Docs' });
    expect(within(dialog).getByText('Changed since you last allowed it')).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('switch', { name: 'Allow this action' }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save review' }));
    await waitFor(() => expect(writes(calls).length).toBe(1));
    expect(JSON.parse(writes(calls)[0].body)).toEqual({ tools: { search_docs: { side_effect: 'read', private: false, disable: true } } });
  });

  it('offers to try again when the server does not start', async () => {
    const calls = await start((m, url) =>
      m === 'GET' && url === '/api/mcp'
        ? [{ name: 'linear', transport: 'http', state: 'error', protocol: '', tools: 0, pending: 0, since: '', error: 'POST https://mcp.linear.app/mcp: 502 Bad Gateway' }]
        : undefined,
    );
    render(McpServer, { params: { item: 'mcp-linear' } });
    expect(await screen.findByText(/^Can't start: the server didn't answer\./)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await waitFor(() => expect(writes(calls).map((c) => `${c.method} ${c.url}`)).toEqual(['POST /api/mcp/linear/reload']));
  });

  it('says plainly when the server is gone', async () => {
    await start();
    render(McpServer, { params: { item: 'mcp-nope' } });
    expect(await screen.findByText("This server doesn't exist or was removed.")).toBeTruthy();
  });
});

describe('Web search (§12.5)', () => {
  it('checks SearXNG automatically and saves it once it works', async () => {
    const calls = await start();
    render(WebSearch);
    await fireEvent.click(await screen.findByRole('radio', { name: /SearXNG/ }));
    expect(await screen.findByText('Search works.')).toBeTruthy();
    expect(writes(calls).map((c) => `${c.method} ${c.url}`)).toEqual(['POST /api/search/test', 'PATCH /api/config']);
    expect(JSON.parse(writes(calls)[1].body).set).toEqual({ 'search.provider': 'searxng', 'search.searxng_url': 'http://searxng:8080' });
  });

  it('offers to allow a private address, then checks again', async () => {
    let allowed = false;
    const calls = await start((m, url) => {
      if (m === 'PATCH' && url === '/api/config') allowed = true;
      if (m === 'POST' && url === '/api/search/test') return allowed ? { ok: true } : { ok: false, code: 'private_address', error: 'egress: blocked' };
    });
    render(WebSearch);
    await fireEvent.click(await screen.findByRole('radio', { name: /SearXNG/ }));
    expect(await screen.findByText('Bothy blocks addresses on your private network unless you allow them.')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Allow searxng:8080' }));
    expect(await screen.findByText('Search works.')).toBeTruthy();
    expect(JSON.parse(writes(calls).find((c) => c.method === 'PATCH')!.body).set).toEqual({ 'search.searxng_allow_private': true });
  });

  it('saves nothing when the check itself fails', async () => {
    const calls = await start((m, url) => (m === 'POST' && url === '/api/search/test' ? new Response('{"error":{"code":"internal","message":"boom"}}', { status: 500 }) : undefined));
    render(WebSearch);
    await fireEvent.click(await screen.findByRole('radio', { name: /SearXNG/ }));
    await waitFor(() => expect(writes(calls).some((c) => c.url === '/api/search/test')).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(writes(calls).some((c) => c.method === 'PATCH')).toBe(false);
    expect(screen.queryByText('Search works.')).toBeNull();
  });

  it('checks a pasted Brave key before saving it', async () => {
    // The mock gateway rejects keys containing "bad".
    const calls = await start();
    render(WebSearch);
    await fireEvent.click(await screen.findByRole('radio', { name: /Brave Search/ }));
    // The saved key is checked first, without sending it.
    await waitFor(() => expect(writes(calls).filter((c) => c.url === '/api/search/test')).toHaveLength(1));
    expect(JSON.parse(writes(calls)[0].body)).toEqual({ provider: 'brave' });
    await fireEvent.click(await screen.findByRole('button', { name: 'Replace' }));
    const key = (await screen.findByLabelText('API key')) as HTMLInputElement;
    await fireEvent.input(key, { target: { value: 'BSA-bad-key' } });
    await fireEvent.keyDown(key, { key: 'Enter' });
    await waitFor(() => expect(writes(calls).filter((c) => c.url === '/api/search/test')).toHaveLength(2));
    expect(JSON.parse(writes(calls).filter((c) => c.url === '/api/search/test')[1].body)).toEqual({ provider: 'brave', api_key: 'BSA-bad-key' });
    expect(await screen.findByText(/That key wasn't accepted/)).toBeTruthy();
    expect(writes(calls).some((c) => c.url.startsWith('/api/vault'))).toBe(false);
  });
});
