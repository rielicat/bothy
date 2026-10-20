import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { member } from '../../../mock/fixtures';
import { resetMock } from '../../../mock/server';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { loadReleases } from './api';
import Releases from './Releases.svelte';

async function open(override?: (m: string, url: string) => unknown, mock = '') {
  installApi({ override });
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  await loadReleases(true);
  router.navigate('/activity/releases');
  render(Releases);
}

describe('Releases (docs/design/ui.md §10.4)', () => {
  it('says who asked and approved, and the commit, from the server', async () => {
    await open();
    await screen.findByText('History');
    const text = document.body.textContent ?? '';
    expect(text).toContain('main · 9e1b3c0 · Add audit export · started 2 min ago');
    expect(text).toContain('Deploy acme/web to production · main at 4f2c1a9');
    expect(text).toContain('You asked · Alex and Maya Chen approved');
    expect(document.body.textContent).not.toMatch(/4f2c1a9d2e8b/);
    // A running deploy is its environment's status, not a History row as well.
    const history = screen.getByText('History').closest('section')!;
    expect(history.textContent).not.toContain('9e1b3c0');
  });

  it('links a waiting request from its environment, whose line 2 is about the last deploy', async () => {
    await open(undefined, 'deploy-waiting');
    const waiting = await screen.findByRole('link', { name: /^Waiting for approval \(1 of 2\)/ }, { timeout: 3000 });
    expect(waiting.getAttribute('href')).toMatch(/^\/approvals\/apr_/);
    expect(waiting.closest('li')!.querySelector('p')!.textContent?.trim()).toMatch(/^Last deployed /);
  });

  it("says the last request was denied, not that the environment is", async () => {
    const now = new Date().toISOString();
    await open((m, url) => {
      if (m !== 'GET') return undefined;
      if (url === '/api/approvals?status=pending') return { items: [] };
      if (url === '/api/approvals?status=recent')
        return {
          items: [
            {
              id: 'apr_denied',
              run_id: 'run_d',
              status: 'denied',
              decided_by: member.id,
              decided_at: now,
              created_at: now,
              approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2, requester: 'usr_01jsomeoneelse000000000000' },
              votes: [{ approval_id: 'apr_denied', user_id: member.id, choice: 'deny', via: 'web', at: now }],
              action: { tool: 'release_deploy', kind: 'release', side_effect: 'irreversible', payload: { repo: 'acme/web', environment: 'production', ref: 'main' }, fingerprint: 'f' },
            },
          ],
        };
      if (url === '/api/releases') {
        const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
        return {
          environments: [{ name: 'production', repo: 'acme/web', workflow: 'deploy.yml', ref_pattern: 'main', inputs: {}, required: 2, approvers: [] }],
          deploys: [
            { id: 'dep_ok', environment: 'production', repo: 'acme/web', workflow: 'deploy.yml', ref: 'main', principal_id: member.id, session_id: 'ses_x', requested_by: member.id,
              approved_by: [], status: 'succeeded', created_at: hourAgo, updated_at: hourAgo, finished_at: hourAgo, sha: 'b'.repeat(40), short_sha: 'bbbbbbb', approved_by_names: [] },
          ],
        };
      }
      return undefined;
    });
    await screen.findByText('History');
    const env = document.querySelector('ul > li')!;
    expect(env.textContent).toContain('Last request denied');
    expect(env.textContent).toMatch(/Last deployed /);
  });

  it('shows a deploy Bothy lost track of as not tracked, and a request its asker withdrew as cancelled', async () => {
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
    await open((m, url) => {
      if (m !== 'GET') return undefined;
      if (url === '/api/approvals?status=pending') return { items: [] };
      if (url === '/api/approvals?status=recent')
        return {
          items: [
            {
              id: 'apr_withdrawn',
              run_id: 'run_w',
              status: 'denied',
              decided_by: member.id,
              comment: 'Wrong branch',
              created_at: new Date().toISOString(),
              requested_by_name: 'Alex',
              approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2, requester: member.id },
              votes: [{ approval_id: 'apr_withdrawn', user_id: member.id, choice: 'deny', via: 'web', at: new Date().toISOString(), comment: 'Wrong branch' }],
              action: { tool: 'release_deploy', kind: 'release', side_effect: 'irreversible', payload: { repo: 'acme/web', environment: 'production', ref: 'main' }, fingerprint: 'f' },
            },
          ],
        };
      if (url === '/api/releases')
        return {
          environments: [{ name: 'production', repo: 'acme/web', workflow: 'deploy.yml', ref_pattern: 'main', inputs: {}, required: 2, approvers: [] }],
          deploys: [
            {
              id: 'dep_lost',
              environment: 'production',
              repo: 'acme/web',
              workflow: 'deploy.yml',
              ref: 'main',
              principal_id: member.id,
              session_id: 'ses_x',
              requested_by: member.id,
              approved_by: [],
              status: 'unknown',
              error: 'GitHub no longer knows the workflow run',
              run_url: 'https://github.com/acme/web/actions/runs/1003',
              created_at: hourAgo,
              updated_at: hourAgo,
              finished_at: new Date().toISOString(),
              sha: 'b'.repeat(40),
              short_sha: 'bbbbbbb',
              requested_by_name: 'Alex',
              approved_by_names: [],
            },
          ],
        };
      return undefined;
    });
    await screen.findByText('History');
    const env = document.querySelector('ul > li')!;
    expect(env.textContent).toContain('Not tracked');
    expect(env.textContent).toContain('Check on GitHub ↗');
    expect(env.textContent).not.toContain('Cancelled');
    const text = document.body.textContent ?? '';
    expect(text).not.toMatch(/Failed|Denied|denied|took/);
    expect(text).toContain('Alex asked · Cancelled by Alex: “Wrong branch”');
    // Once in the History row: beside the title on wide screens, leading the meta line on phones.
    expect(screen.getAllByText('Cancelled').every((el) => el.closest('section'))).toBe(true);
  });

  it('shows every deploy still running and every request that ended without one, linking only what the viewer can open', async () => {
    const at = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
    const dep = (id: string, who: string, min: number, mine: boolean) => ({
      id, environment: 'production', repo: 'acme/web', workflow: 'deploy.yml', ref: 'main', principal_id: who, session_id: mine ? 'ses_' + id : '', run_id: mine ? 'run_' + id : '',
      requested_by: who, approved_by: [], status: 'dispatched', created_at: at(min), updated_at: at(min), finished_at: '', sha: '', short_sha: id, requested_by_name: who, approved_by_names: [],
    });
    const max = dep('max0001', 'Max', 15, false);
    const mine = dep('min0002', member.id, 9, true);
    await open((m, url) => {
      if (m !== 'GET') return undefined;
      if (url === '/api/approvals?status=pending') return { items: [] };
      if (url === '/api/approvals?status=recent')
        return {
          items: [
            {
              id: 'apr_expired', run_id: 'run_e', status: 'expired', created_at: at(30), requested_by_name: 'Sam',
              approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2, requester: 'usr_sam' },
              votes: [{ approval_id: 'apr_expired', user_id: 'usr_priya', choice: 'allow_once', via: 'web', at: at(29) }],
              action: { tool: 'release_deploy', kind: 'release', side_effect: 'irreversible', payload: { repo: 'acme/web', environment: 'production', ref: 'main' }, fingerprint: 'f' },
            },
          ],
        };
      if (url === '/api/releases')
        return { environments: [{ name: 'production', repo: 'acme/web', workflow: 'deploy.yml', ref_pattern: 'main', inputs: {}, required: 2, approvers: [], last: mine, running: [mine, max] }], deploys: [mine] };
      return undefined;
    });
    const history = (await screen.findByText('History')).closest('section')!;
    expect(document.querySelector('ul > li')!.textContent).toContain('2 deploys running');
    expect(history.textContent).toContain('Deploy acme/web to production · main at max0001');
    expect(history.textContent).not.toContain('min0002');
    expect(history.textContent).toContain('Sam asked · only 1 of 2 approvals came in time');
    expect(history.textContent).toContain('Expired');
    // Neither another person's deploy nor their request leads to a task the viewer can't open.
    expect([...history.querySelectorAll('a')].filter((a) => a.getAttribute('href')?.startsWith('/activity/tasks/'))).toEqual([]);
  });

  it('shows a commit subject as quoted text, never as markup', async () => {
    const now = new Date().toISOString();
    await open((m, url) =>
      m === 'GET' && url === '/api/releases'
        ? {
            environments: [{ name: 'staging', repo: 'acme/web', workflow: 'deploy.yml', ref_pattern: 'main', inputs: {}, required: 1, approvers: [] }],
            deploys: [
              {
                id: 'dep_x',
                environment: 'staging',
                repo: 'acme/web',
                workflow: 'deploy.yml',
                ref: 'main',
                principal_id: 'usr_x',
                session_id: 'ses_x',
                requested_by: 'usr_x',
                approved_by: [],
                status: 'succeeded',
                created_at: now,
                updated_at: now,
                finished_at: now,
                sha: 'a'.repeat(40),
                short_sha: 'aaaaaaa',
                subject: '<img src=x onerror=alert(1)> Fix‮ it',
                requested_by_name: 'Sam',
                approved_by_names: [],
              },
            ],
          }
        : undefined,
    );
    const q = await screen.findByText((_, el) => el?.hasAttribute('data-subject') ?? false);
    expect(q.tagName).toBe('Q');
    expect(q.closest('[data-copy-ignore]')).toBeTruthy();
    expect(document.querySelector('img')).toBeNull();
    expect(q.textContent).toContain('<img src=x onerror=alert(1)>');
    expect(q.textContent).toContain('⟨');
  });
});
