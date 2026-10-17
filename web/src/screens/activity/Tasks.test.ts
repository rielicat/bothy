import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import { loadReleases } from './api';
import Tasks from './Tasks.svelte';

async function start(mock = '') {
  const calls = installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  await loadReleases(true);
  router.navigate('/activity');
  render(Tasks);
  await screen.findByRole('heading', { level: 1 });
  return calls;
}

describe('Tasks (docs/design/ui.md §10.1)', () => {
  let calls: Call[];
  beforeEach(async () => {
    calls = await start();
  });

  it('lists tasks by name with money and outcome words, never ids', async () => {
    expect(await screen.findByText('Fix login redirect')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Activity');
    expect(screen.getByText('$0.12')).toBeTruthy();
    expect(screen.getByText('Failed: the GitHub token was rejected')).toBeTruthy();
    expect(screen.getByText('Hit the spending limit')).toBeTruthy();
    expect(screen.getByText('Stopped by Alex')).toBeTruthy();
    expect(screen.getAllByText(/Alex · (Slack|Telegram|API)/).length).toBeGreaterThan(0);
    expect(document.body.textContent).not.toMatch(/\b(usr|ses|run|sch)_[0-9a-z]/);
    expect(calls.some((c) => c.url.startsWith('/api/runs?'))).toBe(true);
  });

  it('filters in the address, and says when nothing matches', async () => {
    await screen.findByText('Fix login redirect');
    await fireEvent.change(screen.getByLabelText('Result'), { target: { value: 'failed' } });
    expect(router.query.get('result')).toBe('failed');
    await waitFor(() => expect(screen.queryByText('Fix login redirect')).toBeNull());
    expect(screen.getByText('Weekly PR digest')).toBeTruthy();
    await fireEvent.change(screen.getByLabelText('Where'), { target: { value: 'api' } });
    expect(await screen.findByText('Nothing matches these filters.')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(await screen.findByText('Fix login redirect')).toBeTruthy();
  });
});

describe('Tasks for a member', () => {
  it('reads "Activity", as for admins, and has no person filter', async () => {
    await start('member');
    expect(await screen.findByText('Draft the release notes')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Activity');
    expect(screen.queryByLabelText('Person')).toBeNull();
    expect(screen.queryByText('Fix login redirect')).toBeNull();
  });

  it('shows the empty state with one action', async () => {
    await start('activity-empty');
    expect(await screen.findByText('Nothing yet. Tasks Bothy works on, in chats or on a schedule, show up here.')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Start a chat' }).getAttribute('href')).toBe('/chat');
  });
});
