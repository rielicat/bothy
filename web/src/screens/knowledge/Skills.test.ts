import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Skills from './Skills.svelte';
import SkillView from './SkillView.svelte';

async function start(mock = '', override?: (m: string, url: string) => unknown) {
  const calls = installApi({ override });
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  return calls;
}

describe('Skills', () => {
  // Turning on names the listed hash; files rescanned since make the server refuse.
  it('turns the switch back off when the server refuses to turn a skill on', async () => {
    const refuse = () => new Response('{"error":{"code":"conflict","message":"the skill files changed; review them again"}}', { status: 409 });
    const calls = await start('', (m, url) => (m === 'PATCH' && url === '/api/skills/onboarding-checklist' ? refuse() : undefined));
    render(Skills);
    // Named by its name, not its folder id.
    const sw = await screen.findByRole('switch', { name: 'Onboarding checklist' });
    expect(sw.getAttribute('aria-checked')).toBe('false');
    await fireEvent.click(sw);
    expect(await screen.findByText('The files of Onboarding checklist changed. Review it before turning it on.')).toBeTruthy();
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET' && c.url === '/api/skills')).toHaveLength(2));
    expect(screen.getByRole('switch', { name: 'Onboarding checklist' }).getAttribute('aria-checked')).toBe('false');
  });

  it('marks a changed skill for review instead of offering its switch', async () => {
    await start();
    render(Skills);
    await screen.findByText('Incident review');
    expect(screen.queryByRole('switch', { name: 'Incident review' })).toBeNull();
    // The button says it; a "Needs review" word beside it would say it twice.
    expect(screen.queryByText('Needs review')).toBeNull();
    expect(screen.getByRole('link', { name: 'Review' }).getAttribute('href')).toBe('/knowledge/skills/incident-review');
  });

  it('shows members the state as a word and no controls', async () => {
    await start('member');
    render(Skills);
    await screen.findByText('Release notes');
    expect(screen.queryAllByRole('switch')).toHaveLength(0);
    expect(screen.getByText('On')).toBeTruthy();
    // A skill waiting for an admin's review reads as off, with nothing a member can act on.
    expect(screen.getAllByText('Off').length).toBeGreaterThan(0);
    expect(screen.queryByText('Needs review')).toBeNull();
    expect(screen.queryByRole('link', { name: 'Review' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Add skill' })).toBeNull();
  });

  it('explains skills when there are none', async () => {
    await start('skills-empty');
    render(Skills);
    expect(await screen.findByText('No skills yet.')).toBeTruthy();
    expect(screen.getByText('Skills teach Bothy how your team does specific things, like writing release notes.')).toBeTruthy();
    // One "Add skill", in the empty state; the header keeps only its ⋯.
    expect(screen.getAllByRole('button', { name: 'Add skill' })).toHaveLength(1);
  });

  it('lets an admin approve a changed skill from its page', async () => {
    const calls = await start();
    render(SkillView, { params: { name: 'incident-review' } });
    await screen.findByText("This skill changed, so it's off until an admin approves the changes below.");
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Incident review');
    // The changed files are in view before approving.
    expect(screen.getByRole('button', { name: 'Files' }).getAttribute('aria-expanded')).toBe('true');
    await fireEvent.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/skills/incident-review')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body)).toEqual({ enabled: true, hash: 'h2b' });
    await waitFor(() => expect(screen.queryByText(/This skill changed/)).toBeNull());
  });
});
