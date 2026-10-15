import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { toaster } from '../../components/ui';
import type { ChannelIdentity } from '../../lib/api/types.gen';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import Schedules from './Schedules.svelte';

// An admin is sent everyone's identities; this one belongs to someone else.
const someoneElses: ChannelIdentity[] = [
  {
    channel: 'telegram',
    external_id: '555001',
    username: 'tester',
    display_name: 'Tester',
    status: 'linked',
    user_id: 'usr_01jtester0000000000000000',
    dm_chat_id: '555001',
    first_seen_at: '2026-10-01T09:00:00Z',
    last_seen_at: '2026-10-14T09:00:00Z',
  },
];

async function start(identities?: ChannelIdentity[]) {
  installApi(identities ? { override: (method, url) => (method === 'GET' && url === '/api/identities' ? identities : undefined) } : {});
  session.ready = false;
  await session.init();
  router.navigate('/activity/schedules');
  render(Schedules);
  return (await screen.findByText('Morning briefing')).closest('li')!.textContent ?? '';
}

describe('Schedules (docs/design/ui.md §10.3)', () => {
  it('says a schedule without a set chat posts to your chat app when you have one linked', async () => {
    expect(await start()).toContain('Posts to your chat app');
  });

  it("says it posts here when only other people's chat apps are linked", async () => {
    expect(await start(someoneElses)).toContain('Posts here');
  });

  it('confirms Run now in a short toast that opens the task', async () => {
    toaster.dismiss();
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'More for Morning briefing' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }));
    await waitFor(() => expect(toaster.current?.text).toBe('Schedule started.'));
    expect(toaster.current?.action?.label).toBe('View');
  });
});
