import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { schedules } from '../../mock/fixtures';
import { session } from '../lib/stores/session.svelte';
import { installApi, type Call } from '../test/api';
import ScheduleEdit from './ScheduleEdit.svelte';

// The server reads a once spec without offset as wall-clock time in the
// schedule's tz, and stores it back as RFC 3339 with that zone's offset.
describe('one-off schedules', () => {
  let calls: Call[];
  const once = { ...schedules[0], id: 'sch_01jonce000000000000000000', name: 'Renew passport', kind: 'once', spec: '2026-10-13T15:00:00-04:00', tz: 'America/New_York' };

  beforeEach(async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-10-13T01:00:00Z'));
    calls = installApi({ override: (m, url) => (m === 'GET' && url === `/api/schedules/${once.id}` ? once : undefined) });
    await session.init();
  });

  afterEach(() => vi.useRealTimers());

  const field = () => screen.getByLabelText('Date and time') as HTMLInputElement;

  it('defaults to an hour from now on the clock of the schedule timezone', async () => {
    render(ScheduleEdit, { params: { id: 'new' } });
    await fireEvent.click(await screen.findByRole('button', { name: 'Once' }));
    // The owner's zone is Europe/London: 02:00Z is 03:00 there in October.
    expect(field().value).toBe('2026-10-13T03:00');
    await waitFor(() => expect(calls.some((c) => c.url === '/api/schedules/preview' && JSON.parse(c.body).spec === '2026-10-13T03:00')).toBe(true), { timeout: 2000 });
  });

  it('shows a saved time in the schedule timezone instead of a blank field', async () => {
    render(ScheduleEdit, { params: { id: once.id } });
    await screen.findByRole('heading', { name: 'Renew passport' });
    expect(field().value).toBe('2026-10-13T15:00');
  });
});
