import { render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { approvals as fixtureApprovals, sessions as fixtureSessions } from '../mock/fixtures';
import type { Me } from './lib/api/types.gen';
import { approvals, session } from './lib/stores/session.svelte';
import Sidebar from './screens/chat/Sidebar.svelte';
import { installApi } from './test/api';

// Keyed {#each} blocks throw on a repeated key, which takes the page down.
// These lists come from the server and can legitimately repeat what the
// original keys assumed unique. The doctor-findings case moved with the
// Advanced page to screens/settings/advanced/Advanced.test.ts.
describe('server lists with repeated keys', () => {
  it('counts an approval once when the pending and parked lists overlap', async () => {
    const both = { items: fixtureApprovals };
    installApi({ override: (m, url) => (url.startsWith('/api/approvals?status=') ? both : undefined) });
    approvals.reset();
    await approvals.load();
    expect(approvals.pending.map((a) => a.id)).toEqual(fixtureApprovals.map((a) => a.id));
  });

  it('shows two group chats that share a name', async () => {
    installApi();
    await session.init();
    const base = session.me!;
    const space = (id: string) => ({ id, kind: 'space', user_id: base.user.id, display_name: 'Family', workspace_id: 'wsp', tool_profile: 'restricted', created_at: '' });
    session.me = { ...base, principals: [...base.principals, space('spc_a'), space('spc_b')] } as Me;
    const s0 = fixtureSessions[0];
    const list = { items: [{ ...s0, id: 'ses_a', principal_id: 'spc_a' }, { ...s0, id: 'ses_b', principal_id: 'spc_b' }] };
    installApi({ override: (m, url) => (url.startsWith('/api/sessions?') ? list : undefined) });
    render(Sidebar);
    await waitFor(() => expect(screen.getAllByRole('heading', { name: 'Family' })).toHaveLength(2));
  });
});
