import { render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Approval } from '../../lib/api/types.gen';
import { installApi } from '../../test/api';
import DeployFollowUp from './DeployFollowUp.svelte';

const approval = { id: 'apr_dep', status: 'allowed', action: { tool: 'release_deploy' } } as unknown as Approval;
const deploy = (status: string, extra: Record<string, unknown> = {}) => ({
  id: 'dep_1',
  approval_id: approval.id,
  environment: 'production',
  repo: 'acme/web',
  ref: 'main',
  status,
  run_url: 'https://github.com/acme/web/actions/runs/1003',
  created_at: '2026-10-14T10:00:00Z',
  ...extra,
});

function follow(d: ReturnType<typeof deploy>) {
  const calls = installApi({ override: (m, url) => (m === 'GET' && url === '/api/releases' ? { environments: [], deploys: [d] } : undefined) });
  render(DeployFollowUp, { approval });
  return () => calls.filter((c) => c.url === '/api/releases').length;
}

describe('DeployFollowUp (docs/design/ui.md §8.7)', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('keeps checking a deploy that is still running', async () => {
    vi.useFakeTimers();
    const reads = follow(deploy('running'));
    await vi.waitFor(() => expect(screen.getByRole('status').textContent).toContain('Deploying acme/web to production… · View on GitHub ↗'));
    await vi.advanceTimersByTimeAsync(15_000);
    expect(reads()).toBeGreaterThan(1);
  });

  it('says Bothy stopped tracking a deploy it lost, links to GitHub, and stops asking', async () => {
    vi.useFakeTimers();
    const reads = follow(deploy('unknown', { finished_at: '2026-10-14T11:00:00Z', error: 'GitHub no longer knows the workflow run' }));
    const line = await vi.waitFor(() => {
      const s = screen.getByRole('status');
      expect(s.textContent).toContain('Bothy stopped tracking the deploy of acme/web to production · Check on GitHub ↗');
      return s;
    });
    expect(line.textContent).not.toContain('Deploying');
    expect(line.querySelector('.animate-spin')).toBeNull();
    expect(line.querySelector('.text-danger-text')).toBeNull();
    expect(screen.getByRole('link').getAttribute('href')).toBe('https://github.com/acme/web/actions/runs/1003');
    await vi.advanceTimersByTimeAsync(60_000);
    expect(reads()).toBe(1);
  });
});
