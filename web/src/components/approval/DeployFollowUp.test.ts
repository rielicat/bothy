import { render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Approval } from '../../lib/api/types.gen';
import { events } from '../../lib/sse.svelte';
import { installApi } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
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

// Answers /api/releases with whatever `list` holds at the time, and counts the reads.
function serve(list: () => ReturnType<typeof deploy>[]) {
  const calls = installApi({ override: (m, url) => (m === 'GET' && url === '/api/releases' ? { environments: [], deploys: list() } : undefined) });
  return () => calls.filter((c) => c.url === '/api/releases').length;
}

function follow(d: ReturnType<typeof deploy>) {
  const reads = serve(() => [d]);
  render(DeployFollowUp, { approval });
  return reads;
}

describe('DeployFollowUp (docs/design/ui.md §8.7)', () => {
  afterEach(() => {
    vi.useRealTimers();
    events.close();
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

  it('shares one read of the deploy list between every line on screen', async () => {
    vi.useFakeTimers();
    const list = [
      deploy('running', { id: 'dep_a', approval_id: 'apr_a', environment: 'staging' }),
      deploy('running', { id: 'dep_b', approval_id: 'apr_b', environment: 'production' }),
      deploy('succeeded', { id: 'dep_c', approval_id: 'apr_c', environment: 'preview', finished_at: '2026-10-14T10:03:00Z' }),
    ];
    const reads = serve(() => list);
    for (const id of ['apr_a', 'apr_b', 'apr_c']) render(DeployFollowUp, { approval: { ...approval, id } });
    await vi.waitFor(() => expect(screen.getAllByRole('status')).toHaveLength(3));
    expect(screen.getAllByRole('status').map((s) => s.textContent?.trim())).toEqual([
      expect.stringContaining('Deploying acme/web to staging'),
      expect.stringContaining('Deploying acme/web to production'),
      'Deployed acme/web to preview in 3 min',
    ]);
    expect(reads()).toBe(1);
    await vi.advanceTimersByTimeAsync(15_000);
    expect(reads()).toBe(2);
  });

  it('forgets the deploy list once no line shows it, so the next screen starts from its own read', async () => {
    serve(() => [deploy('succeeded', { finished_at: '2026-10-14T10:03:00Z' })]);
    const first = render(DeployFollowUp, { approval });
    await vi.waitFor(() => expect(screen.getByRole('status')).toBeTruthy());
    first.unmount();
    // Someone else signs in on this tab and can see the same request.
    let answered = false;
    installApi({ override: (m, url) => (m === 'GET' && url === '/api/releases' ? ((answered = true), { environments: [], deploys: [] }) : undefined) });
    render(DeployFollowUp, { approval });
    expect(screen.queryByRole('status')).toBeNull();
    await vi.waitFor(() => expect(answered).toBe(true));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('drops an answer that arrives after every line is gone', async () => {
    let answer: ((r: Response) => void) | undefined;
    vi.stubGlobal('fetch', () => new Promise<Response>((r) => (answer = r)));
    const first = render(DeployFollowUp, { approval });
    await vi.waitFor(() => expect(answer).toBeTypeOf('function'));
    first.unmount();
    answer!(new Response(JSON.stringify({ environments: [], deploys: [deploy('running')] }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    await new Promise((r) => setTimeout(r, 20));
    serve(() => []);
    render(DeployFollowUp, { approval });
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('reads the deploy list again as soon as a release event says the deploy moved', async () => {
    events.connect();
    let d = deploy('running');
    const reads = serve(() => [d]);
    render(DeployFollowUp, { approval });
    await vi.waitFor(() => expect(screen.getByRole('status').textContent).toContain('Deploying acme/web to production'));
    d = deploy('succeeded', { finished_at: '2026-10-14T10:03:00Z' });
    // An admin who asked hears it twice: as the requester and as an admin.
    for (const seq of [7, 8]) {
      FakeEventSource.last.emit('release.finished', { seq, type: 'release.finished', at: new Date().toISOString(), data: { deploy_id: d.id, status: 'succeeded', ok: true } });
    }
    await vi.waitFor(() => expect(screen.getByRole('status').textContent?.trim()).toBe('Deployed acme/web to production in 3 min'));
    await new Promise((r) => setTimeout(r, 50));
    expect(reads()).toBe(2);
  });
});
