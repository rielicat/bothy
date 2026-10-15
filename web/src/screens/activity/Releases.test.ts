import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { member } from '../../../mock/fixtures';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { loadReleases } from './api';
import Releases from './Releases.svelte';

async function open(override?: (m: string, url: string) => unknown) {
  installApi({ override });
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
    expect(screen.getAllByText('Cancelled')).toHaveLength(1);
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
