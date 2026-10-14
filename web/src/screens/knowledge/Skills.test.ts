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
    const sw = await screen.findByRole('switch', { name: 'onboarding-checklist' });
    expect(sw.getAttribute('aria-checked')).toBe('false');
    await fireEvent.click(sw);
    expect(await screen.findByText('The files of onboarding-checklist changed. Review it before turning it on.')).toBeTruthy();
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET' && c.url === '/api/skills')).toHaveLength(2));
    expect(screen.getByRole('switch', { name: 'onboarding-checklist' }).getAttribute('aria-checked')).toBe('false');
  });

  it('marks a changed skill for review instead of offering its switch', async () => {
    await start();
    render(Skills);
    await screen.findByText('incident-review');
    expect(screen.queryByRole('switch', { name: 'incident-review' })).toBeNull();
    expect(screen.getByText('Needs review')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Review' }).getAttribute('href')).toBe('/knowledge/skills/incident-review');
  });

  it('shows members the state as a word and no controls', async () => {
    await start('member');
    render(Skills);
    await screen.findByText('release-notes');
    expect(screen.queryAllByRole('switch')).toHaveLength(0);
    expect(screen.getByText('On')).toBeTruthy();
    expect(screen.getByText('Off')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Add skill' })).toBeNull();
  });

  it('explains skills when there are none', async () => {
    await start('skills-empty');
    render(Skills);
    expect(await screen.findByText('No skills yet.')).toBeTruthy();
    expect(screen.getByText('Skills teach Bothy how your team does specific things, like writing release notes.')).toBeTruthy();
  });

  it('lets an admin approve a changed skill from its page', async () => {
    const calls = await start();
    render(SkillView, { params: { name: 'incident-review' } });
    await screen.findByText("Its files changed since an admin approved it, so it's off until someone reviews it.");
    await fireEvent.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/skills/incident-review')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body)).toEqual({ enabled: true, hash: 'h2b' });
    await waitFor(() => expect(screen.queryByText(/Its files changed/)).toBeNull());
  });
});
