import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { phrases } from '../../lib/activity';
import type { Approval } from '../../lib/api/types.gen';
import DecidedLine from './DecidedLine.svelte';

const deploy = phrases({ tool: 'release_deploy', args: { repo: 'acme/web', environment: 'production' } });
const base = { id: 'apr_1', status: 'denied', decided_by: 'usr_admin', decided_at: '2026-10-14T22:28:00Z' } as unknown as Approval;

describe('DecidedLine', () => {
  it("names a decider the viewer can't look up, with their reason", () => {
    const { container } = render(DecidedLine, {
      approval: { ...base, approvals: { required: 2, eligible: [], any_admin: true, votes: [{ user: 'usr_admin', name: 'Tess', choice: 'deny', at: base.decided_at!, comment: 'Freeze until Thursday' }] } } as Approval,
      phrases: deploy,
    });
    expect(container.textContent).toContain('Tess denied deploying acme/web to production · “Freeze until Thursday”');
    expect(container.textContent).not.toContain('Someone');
  });

  it('says the person who asked cancelled it, not that they denied it', () => {
    const { container } = render(DecidedLine, {
      approval: {
        ...base,
        decided_by: 'usr_tess',
        approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2, requester: 'usr_tess' },
        approvals: { required: 2, eligible: [], any_admin: true, votes: [{ user: 'usr_tess', name: 'Tess', choice: 'deny', at: base.decided_at! }] },
      } as Approval,
      phrases: deploy,
    });
    expect(container.textContent).toContain('Cancelled by Tess');
    expect(container.textContent).not.toContain('denied');
  });

  it('credits everyone who approved a two-person request', () => {
    const votes = [
      { user: 'usr_p', name: 'Priya Shah', choice: 'allow_once', at: base.decided_at! },
      { user: 'usr_s', name: 'Sam Lee', choice: 'allow_once', at: base.decided_at! },
    ];
    const { container } = render(DecidedLine, {
      approval: { ...base, status: 'allowed', choice: 'allow_once', decided_by: 'usr_s', approvals: { required: 2, eligible: [], any_admin: true, votes } } as unknown as Approval,
      phrases: deploy,
    });
    expect(container.textContent).toContain('Priya Shah and Sam Lee approved deploying acme/web to production');
  });
});
