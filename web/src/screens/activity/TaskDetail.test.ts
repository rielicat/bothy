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
  return render(TaskDetail, { params: { id } });
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

  // A refined answer's drafts come before its model calls, say which draft was sent and what
  // "agreed" counts, and read as text rather than code.
  it('leads Technical details with the drafts of a refined answer', async () => {
    await open('run_01jpricing00000000000000');
    const tech = await screen.findByRole('button', { name: 'Technical details' });
    tech.click();
    const summary = await screen.findByText('3 drafts, 2 rounds. Draft 2 was sent; 2 of 3 drafts reached its answer.');
    const headings = [...document.querySelectorAll('h3')].map((h) => h.textContent);
    expect(headings.indexOf('Refine')).toBeLessThan(headings.indexOf('Model call 1'));
    expect(summary).toBeTruthy();
    (await screen.findByRole('button', { name: 'Correction 2, round 2' })).click();
    const text = await screen.findByText('Raise the team plan to $24 from January …');
    expect(text.closest('pre, code')).toBeNull();
    // Small words on a guide line under their toggle, so a draft doesn't read as the page's own text.
    expect(text.closest('.md-small')?.parentElement?.classList.contains('border-l')).toBe(true);
  });

  it('lists the steps as rows of one group card, each opening its details', async () => {
    await open('run_01jfixlogin0000000000000');
    const step = await screen.findByRole('button', { name: /^Ran npm test/ });
    expect(step.closest('ul')?.classList.contains('list-card')).toBe(true);
    expect(step.getAttribute('aria-expanded')).toBe('false');
    step.click();
    await screen.findByText(/^How it was allowed/);
    expect(step.getAttribute('aria-expanded')).toBe('true');
  });

  // Every step finished, so no row needs a state glyph and the words start at the card's edge.
  it('keeps a glyph column only when some step has a glyph', async () => {
    const view = await open('run_01jfixlogin0000000000000');
    const done = await screen.findByRole('button', { name: /^Ran npm test/ });
    expect(done.querySelector('.w-4')).toBeNull();
    view.unmount();
    await open('run_01jdeployweb000000000000');
    const waiting = await screen.findByRole('button', { name: /^Waiting for approval to deploy/ });
    const rows = [...waiting.closest('ul')!.querySelectorAll('li > div > button')];
    expect(rows.length).toBeGreaterThan(1);
    expect(rows.every((r) => r.querySelector('.w-4'))).toBe(true);
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
