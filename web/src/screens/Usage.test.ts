import { render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { router } from '../lib/router.svelte';
import { session } from '../lib/stores/session.svelte';
import { installApi, type Call } from '../test/api';
import Usage from './Usage.svelte';

// GET /api/usage?group_by=day keys rows by UTC date (store usageKeys).
describe('usage by day', () => {
  let calls: Call[];
  const tz = process.env.TZ;
  const row = { key: '2026-10-13', cost_micros: 800_000, input: 1000, output: 100, cache_read: 0, cache_write: 0, calls: 1, price_known: true };

  beforeEach(async () => {
    process.env.TZ = 'America/New_York';
    vi.useFakeTimers({ toFake: ['Date'] });
    // 21:00 on 12 October in New York
    vi.setSystemTime(new Date('2026-10-13T01:00:00Z'));
    calls = installApi({
      override: (_m, url) => {
        if (!url.startsWith('/api/usage?')) return undefined;
        const day = new URLSearchParams(url.split('?')[1]).get('group_by') === 'day';
        return { rows: day ? [row] : [], totals: { ...row, key: 'total' } };
      },
    });
    await session.init();
  });

  afterEach(() => {
    vi.useRealTimers();
    process.env.TZ = tz;
  });

  it('draws spend on the UTC day the server reports it under', async () => {
    router.navigate('/usage?range=1');
    render(Usage);
    const titles = () => [...document.querySelectorAll('rect title')].map((t) => t.textContent);
    await waitFor(() => expect(titles()).toEqual(['Oct 13: $0.8']));
    expect(screen.getByText(/UTC/)).toBeTruthy();
    const from = new URL(calls.find((c) => c.url.includes('group_by=day'))!.url, 'http://bothy').searchParams.get('from');
    expect(from).toBe('2026-10-13T00:00:00.000Z');
  });
});
