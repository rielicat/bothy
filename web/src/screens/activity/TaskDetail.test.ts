import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import TaskDetail from './TaskDetail.svelte';

async function open(id: string, mock = '') {
  installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  router.navigate(`/activity/tasks/${id}`);
  render(TaskDetail, { params: { id } });
}

describe('Task detail (docs/design/ui.md §10.2)', () => {
  it('offers the way forward from a step waiting for the viewer', async () => {
    await open('run_01jdeployweb000000000000');
    const review = await screen.findByRole('link', { name: 'Review it' });
    expect(review.getAttribute('href')).toMatch(/^\/approvals\/apr_/);
  });

  it('points an admin at the GitHub connection when GitHub refused Bothy, which Open chat cannot fix', async () => {
    await open('run_01jprdigest1000000000000');
    const fix = await screen.findByRole('link', { name: 'Check the GitHub connection ›' });
    expect(fix.getAttribute('href')).toBe('/settings/connections/github');
  });

  it("doesn't send a member to settings they can't change", async () => {
    session.me = null;
    await open('run_01jprdigest1000000000000');
    await screen.findByRole('link', { name: 'Check the GitHub connection ›' });
    session.me!.user.role = 'member';
    await screen.findByText("Couldn't check builds in acme/web", { exact: false });
    expect(screen.queryByRole('link', { name: 'Check the GitHub connection ›' })).toBeNull();
    session.me!.user.role = 'owner';
  });

  // The server answers 404 both for a deleted task and for someone else's (internal/httpapi/idor_test.go).
  it('says a task it cannot open may be deleted or belong to someone else', async () => {
    await open('run_missing');
    expect(await screen.findByRole('heading', { name: "Can't open this task" })).toBeTruthy();
    expect(screen.getByText('This task was deleted, or it belongs to someone else.')).toBeTruthy();
    expect(screen.queryByText(/not found|doesn't exist/i)).toBeNull();
    expect(screen.getByRole('link', { name: 'Go to Activity' }).getAttribute('href')).toBe('/activity');
  });
});
