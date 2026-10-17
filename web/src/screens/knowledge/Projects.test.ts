import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import KnowledgeSearch from './KnowledgeSearch.svelte';
import Projects from './Projects.svelte';

async function start(mock = '', override?: (m: string, url: string) => unknown) {
  const calls = installApi({ override });
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  return calls;
}

describe('Projects', () => {
  it('lists each repository with its state in words', async () => {
    await start();
    render(Projects);
    const list = await screen.findByRole('list', { name: 'Projects' });
    const row = (name: string) => within(list).getByRole('link', { name }).closest('li') as HTMLElement;
    expect(within(row('acme/web')).getByText('Up to date')).toBeTruthy();
    expect(within(row('acme/web')).getByText(/^Customer-facing web app, deployed from main\. Notes updated/)).toBeTruthy();
    expect(row('acme/api').textContent).toContain('Reading… 40%');
    expect(within(row('acme/infra')).getByText("Couldn't read")).toBeTruthy();
    expect(within(row('acme/infra')).getByText('Too large to read (more than 50,000 files).')).toBeTruthy();
    // Too big fails the same way every time: no Details, no Read again.
    expect(within(row('acme/infra')).queryByRole('button', { name: 'Details' })).toBeNull();
    expect(within(row('acme/infra')).queryByRole('button', { name: 'Read again' })).toBeNull();
    expect(within(row('acme/docs')).getByText('Out of date')).toBeTruthy();
    // Searching code and notes lives on this tab, under the tabs, once something was read.
    const search = screen.getByRole('searchbox');
    expect(screen.getByRole('link', { name: 'Skills' }).compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(row('acme/docs')).getByRole('button', { name: 'Read again' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Add repositories' })).toBeTruthy();
  });

  it("offers to read a connected repository this person's Bothy hasn't read", async () => {
    const calls = await start('member', (m, url) =>
      m === 'GET' && url.startsWith('/api/knowledge/repos') ? Response.json({ items: [] }) : m === 'POST' && url === '/api/github/repos/read' ? Response.json({ repos: ['acme/web'] }, { status: 202 }) : undefined,
    );
    render(Projects);
    const list = await screen.findByRole('list', { name: 'Projects' });
    const first = within(list).getAllByRole('listitem')[0];
    expect(first.textContent).toContain('Not read yet');
    expect(first.textContent).not.toContain('Reading');
    await fireEvent.click(within(first).getByRole('button', { name: 'Read now' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/github/repos/read')).toBe(true));
    expect(first.textContent).toContain('Reading…');
  });

  it('gives members no Read again', async () => {
    await start('member');
    render(Projects);
    await screen.findByText('acme/docs');
    expect(screen.queryByRole('button', { name: 'Read again' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Add repositories' })).toBeNull();
  });

  it.each([
    ['knowledge-empty,github-none', 'Connect GitHub to give Bothy your code.', 'Connect GitHub'],
    ['knowledge-empty,github-none,member', 'Ask an admin to connect GitHub so Bothy can read your code.', null],
    ['knowledge-empty,github-no-repos', 'Choose which repositories Bothy can read.', 'Choose repositories'],
  ])('says what to do next when empty (%s)', async (mock, text, action) => {
    await start(mock);
    render(Projects);
    expect(await screen.findByText(text)).toBeTruthy();
    // Nothing read yet, so nothing to search.
    expect(screen.queryByRole('searchbox')).toBeNull();
    if (action) expect(screen.getByRole('link', { name: action }).getAttribute('href')).toMatch(/^\/settings\/connections\/github/);
    else expect(screen.queryByRole('link', { name: /GitHub|repositories/ })).toBeNull();
    // The empty state carries the one action; the header doesn't repeat it.
    expect(screen.queryByRole('link', { name: 'Add repositories' })).toBeNull();
  });

  it('shows how to connect GitHub, not an error, before the server has Knowledge', async () => {
    await start('github-none', (_m, url) => (url.startsWith('/api/knowledge/') ? new Response('{"error":{"code":"unavailable","message":"no"}}', { status: 503 }) : undefined));
    render(Projects);
    expect(await screen.findByText('Connect GitHub to give Bothy your code.')).toBeTruthy();
    expect(screen.queryByText(/didn't load/)).toBeNull();
  });
});

describe('Knowledge search', () => {
  it('groups code by file, filters to notes, and names the empty result', async () => {
    await start();
    router.navigate('/knowledge/search?q=login%20redirect');
    render(KnowledgeSearch);
    const results = await screen.findByRole('list', { name: 'Results' });
    expect(within(results).getByText('src/auth/redirect.ts')).toBeTruthy();
    expect(within(results).getAllByText('login').length).toBeGreaterThan(0);
    expect(results.querySelectorAll('mark').length).toBeGreaterThan(2);
    expect(results.textContent).not.toContain('#L10');

    router.navigate('/knowledge/search?q=login%20redirect&type=notes');
    await screen.findByText(/Project notes/);
    expect(screen.queryByText('src/auth/redirect.ts')).toBeNull();

    router.navigate('/knowledge/search?q=kubernetes');
    expect(await screen.findByText('Nothing in your projects or notes matches “kubernetes”.')).toBeTruthy();
  });
});
