import { render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { resetMock } from '../../../mock/server';
import { gridlines } from '../../components/BarChart.svelte';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import { loadReleases } from './api';
import Spending from './Spending.svelte';

async function start(path: string, opts: { mock?: string; override?: (m: string, url: string) => unknown } = {}): Promise<Call[]> {
  const calls = installApi({ override: opts.override });
  if (opts.mock) resetMock({ mock: opts.mock });
  session.ready = false;
  await session.init();
  await loadReleases(true);
  router.navigate(path);
  render(Spending);
  return calls;
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllEnvs();
});

describe('Spending (docs/design/ui.md §10.5)', () => {
  it('leads with one sentence in money and lists names, never ids', async () => {
    await start('/activity/spending');
    expect(await screen.findByText(/in the last 30 days$/)).toBeTruthy();
    expect(screen.getByText('Today: $1.20 of the $5.00 daily limit')).toBeTruthy();
    expect(screen.getByRole('progressbar').getAttribute('aria-valuetext')).toBe('$1.20 of $5.00');
    expect(screen.getByText('$18.20')).toBeTruthy();
    expect(screen.getByText('You')).toBeTruthy();
    expect(screen.getByText('Claude Sonnet 4.6 · Anthropic')).toBeTruthy();
    expect(screen.getByText('Free')).toBeTruthy();
    expect(screen.getByText('Price not set')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Set price' })).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/\b(usr|ses|sch)_[0-9a-z]|micro|tokens? (in|out)/i);
  });

  it('makes every bar focusable and matches the hidden table to the bars', async () => {
    await start('/activity/spending?range=7');
    await screen.findByText(/in the last 7 days$/);
    const bars = screen.getAllByRole('img');
    expect(bars).toHaveLength(7);
    for (const b of bars) expect(b.getAttribute('tabindex')).toBe('0');
    const rows = [...document.querySelectorAll('.sr-only table tbody tr')].map((r) => [...r.querySelectorAll('td')].map((td) => td.textContent).join(': '));
    expect(rows).toEqual(bars.map((b) => b.getAttribute('aria-label')));
    expect(screen.getByRole('group').getAttribute('aria-label')).toMatch(/^Spending by day, .+ to .+, highest \$[\d.]+ on .+$/);
  });

  it('draws spend on the UTC day the server reports it under', async () => {
    vi.stubEnv('TZ', 'America/New_York');
    vi.useFakeTimers({ toFake: ['Date'] });
    // 21:00 on 12 October in New York is already 13 October in UTC.
    vi.setSystemTime(new Date('2026-10-13T01:00:00Z'));
    const row = { key: '2026-10-13', cost_micros: 800_000, input: 1000, output: 100, cache_read: 0, cache_write: 0, calls: 1, price_known: true };
    const calls = await start('/activity/spending?range=7', {
      override: (_m, url) => {
        if (!url.startsWith('/api/usage?')) return undefined;
        const day = new URLSearchParams(url.split('?')[1]).get('group_by') === 'day';
        return { rows: day ? [row] : [], totals: { ...row, key: '' } };
      },
    });
    await waitFor(() => expect(screen.getAllByRole('img').at(-1)?.getAttribute('aria-label')).toMatch(/Oct 13: \$0\.80$/));
    const from = new URL(calls.find((c) => c.url.includes('group_by=day'))!.url, 'http://bothy').searchParams.get('from');
    expect(from).toBe('2026-10-07T00:00:00.000Z');
  });

  it('labels two gridlines at round amounts under the highest day', () => {
    expect(gridlines(4_100_000)).toEqual([2_000_000, 4_000_000]);
    expect(gridlines(2_500_000)).toEqual([1_000_000, 2_000_000]);
    expect(gridlines(1_200_000)).toEqual([500_000, 1_000_000]);
    expect(gridlines(30_000)).toEqual([10_000, 20_000]);
    expect(gridlines(0)).toEqual([]);
  });

  it("doesn't repeat today's amount in the Today view", async () => {
    await start('/activity/spending?range=today');
    expect(await screen.findByText('$1.20 today')).toBeTruthy();
    expect(screen.getByText('The daily limit is $5.00.')).toBeTruthy();
    expect(screen.queryByText(/^Today: /)).toBeNull();
  });

  it('warns at 80 % and stops at the limit', async () => {
    await start('/activity/spending', { mock: 'spend-80' });
    expect(await screen.findByText('Bothy will stop starting new work at $5.00.')).toBeTruthy();
  });

  it('says when work waits for midnight, with Change limit for admins', async () => {
    await start('/activity/spending', { mock: 'spend-limit' });
    expect(await screen.findByText(/Bothy reached today's limit\. New work waits until midnight\./)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Change limit' })).toBeTruthy();
  });

  it('shows a member only their own spending', async () => {
    await start('/activity/spending', { mock: 'member' });
    expect(await screen.findByRole('heading', { level: 1, name: 'Your spending' })).toBeTruthy();
    await screen.findByText('By model');
    expect(screen.queryByText('By person')).toBeNull();
  });
});
