import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { installApi } from '../../test/api';
import NewChat from './NewChat.svelte';

const rows = [
  { text: 'Summarise the open pull requests in acme/web' },
  { text: 'Research a topic on the web', fill: 'Research this on the web and give me a short summary with links to sources: ' },
  { text: 'Connect GitHub so Bothy can read your code', href: '/settings/connections/github' },
  { text: 'What can you do for our team?' },
];

describe('starters (§7.2)', () => {
  it("shows the server's rows: prompts fill the composer, a set-up row is a link", async () => {
    installApi({ override: (_m, url) => (url === '/api/starters' ? rows : undefined) });
    const onpick = vi.fn();
    render(NewChat, { onpick });
    await fireEvent.click(await screen.findByRole('button', { name: 'Research a topic on the web' }));
    expect(onpick).toHaveBeenLastCalledWith('Research this on the web and give me a short summary with links to sources: ');
    await fireEvent.click(screen.getByRole('button', { name: 'Summarise the open pull requests in acme/web' }));
    expect(onpick).toHaveBeenLastCalledWith('Summarise the open pull requests in acme/web');
    expect(screen.getByRole('link', { name: 'Connect GitHub so Bothy can read your code' }).getAttribute('href')).toBe('/settings/connections/github');
    expect(screen.queryByRole('button', { name: 'Connect GitHub so Bothy can read your code' })).toBeNull();
    expect(screen.getAllByRole('listitem')).toHaveLength(4);
  });

  it('keeps room for four rows while they load, so nothing below moves', async () => {
    vi.stubGlobal('fetch', () => new Promise(() => {}));
    render(NewChat, { onpick: () => {} });
    expect(screen.getByRole('list').parentElement!.classList.contains('min-h-44')).toBe(true);
    expect(screen.getByRole('list').classList.contains('list-card')).toBe(false);
    expect(screen.queryAllByRole('listitem')).toHaveLength(0);
  });

  it('offers only "What can you do for our team?" when the rows fail to load', async () => {
    installApi({ override: (_m, url) => (url === '/api/starters' ? new Response('{"error":{"code":"internal","message":"x"}}', { status: 500 }) : undefined) });
    render(NewChat, { onpick: () => {} });
    expect(await screen.findByRole('button', { name: 'What can you do for our team?' })).toBeTruthy();
    expect(screen.getAllByRole('listitem')).toHaveLength(1);
  });

  it('heads the empty chat, and is only the rows when bare', async () => {
    installApi({ override: (_m, url) => (url === '/api/starters' ? rows : undefined) });
    const empty = render(NewChat, { onpick: () => {} });
    expect(screen.getByRole('heading', { level: 1, name: 'What should Bothy work on?' })).toBeTruthy();
    empty.unmount();
    render(NewChat, { onpick: () => {}, bare: true });
    await screen.findByRole('button', { name: 'What can you do for our team?' });
    expect(screen.queryByRole('heading')).toBeNull();
    expect(screen.queryByText(/^Bothy can read your code, draft and research/)).toBeNull();
  });
});
