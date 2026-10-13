import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Skills from './Skills.svelte';

describe('Skills', () => {
  // Enabling names the listed hash; files rescanned since make the server refuse.
  it('turns the switch back off when the server refuses to enable a skill', async () => {
    const refuse = () => new Response('{"error":{"code":"conflict","message":"the skill files changed; review them again"}}', { status: 409 });
    const calls = installApi({ override: (m, url) => (m === 'PATCH' && url === '/api/skills/trip-notes' ? refuse() : undefined) });
    await session.init();
    render(Skills);
    const box = (await screen.findByRole('checkbox', { name: 'Enable trip-notes' })) as HTMLInputElement;
    expect(box.checked).toBe(false);
    await fireEvent.click(box);
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET' && c.url === '/api/skills')).toHaveLength(2));
    expect(box.checked).toBe(false);
  });
});
