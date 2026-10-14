import { describe, expect, it } from 'vitest';
import { approvals as fixtureApprovals } from '../mock/fixtures';
import { approvals } from './lib/stores/session.svelte';
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
});
