import { waitFor } from '@testing-library/svelte';
import { expect, it } from 'vitest';
import { owner } from '../../../mock/fixtures';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { people } from './people.svelte';

it('asks for names once, for the chat and the approval cards alike', async () => {
  const tess = { id: 'usr_tess', username: 'tess', display_name: 'Tess' };
  const calls = installApi({ override: (m, url) => (m === 'GET' && url === '/api/users' ? [tess] : undefined) });
  session.me = { user: owner, csrf: 'mock-csrf', mode: 'multi', principals: [], paused: false, sandbox_available: true, banners: [], access: { peer: 'local', tls: false } };
  people.load();
  expect(people.known(tess.id)).toBe('');
  await waitFor(() => expect(people.name(tess.id)).toBe('Tess'));
  expect(people.known(tess.id)).toBe('Tess');
  expect(people.known(owner.id)).toBe(owner.display_name || owner.username);
  expect(people.name(owner.id)).toBe('You');
  expect(calls.filter((c) => c.url === '/api/users')).toHaveLength(1);
});
