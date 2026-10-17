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

  it('names what a saved permission lets through, not only the one action', () => {
    const rm = { tool: 'shell', kind: 'exec', side_effect: 'irreversible', payload: { argv: ['rm', '-rf', 'build'] }, fingerprint: '' };
    const approval = { ...base, status: 'allowed', decided_by_name: 'Tess', action: rm, card: { title: 'Run rm -rf build?', fields: [] } } as unknown as Approval;
    const p = phrases({ tool: 'shell', action: approval.action });
    const session = render(DecidedLine, { approval: { ...approval, choice: 'allow_session' }, phrases: p });
    expect(session.container.textContent).toContain('Tess allowed running rm commands for this chat');
    session.unmount();
    const always = render(DecidedLine, { approval: { ...approval, choice: 'allow_always' }, phrases: p });
    expect(always.container.textContent).toContain('Tess always allows running rm commands');
    expect(always.container.textContent).not.toContain('rm -rf build');
  });

  it("says only \"approved it\" right under the step's own line, and keeps a grant's words", () => {
    const write = phrases({ tool: 'fs_write', args: { path: 'repos/acme/web/src/auth/callback.ts', mode: 'overwrite' } });
    const allowed = { ...base, status: 'allowed', choice: 'allow_once', decided_by_name: 'Tess' } as unknown as Approval;
    const after = render(DecidedLine, { approval: allowed, phrases: write, afterStep: true });
    expect(after.container.textContent?.trim()).toMatch(/^Tess approved it ·/);
    after.unmount();
    const alone = render(DecidedLine, { approval: allowed, phrases: write });
    expect(alone.container.textContent).toContain('Tess approved changing acme/web · src/auth/callback.ts');
  });

  it("says what a stop cancelled, and keeps the time and Show together", () => {
    const msg = phrases({ tool: 'message', args: { text: 'Deploy done' }, action: { tool: 'message', kind: 'send', side_effect: 'external_send', recipient: 'slack:C1', fingerprint: '' } as never });
    const { container } = render(DecidedLine, { approval: { ...base, status: 'cancelled', decided_via: 'stop' } as Approval, phrases: msg });
    expect(container.textContent?.trim()).toMatch(/^Cancelled: Bothy didn't send a message to the Slack chat · \S+/);
    const tail = container.querySelector('.whitespace-nowrap')!;
    expect(tail.textContent).toMatch(/^· .+ · Show$/);
  });
});
