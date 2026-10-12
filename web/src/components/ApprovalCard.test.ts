import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { owner } from '../../mock/fixtures';
import { setCsrf } from '../lib/api/client';
import type { Approval } from '../lib/api/types.gen';
import { approvals, session } from '../lib/stores/session.svelte';
import { installApi, type Call } from '../test/api';
import ApprovalCard from './ApprovalCard.svelte';

function approval(extra: Partial<Approval> = {}): Approval {
  return {
    id: 'apr_01jshell00000000000000000',
    run_id: 'run_1',
    session_id: 'ses_1',
    principal_id: owner.id,
    tool_call_id: 'tcl_1',
    // Model-controlled text lives in the Action payload; the card must not show it.
    action: { tool: 'shell', kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', 'rm -rf build'], payload: { command: 'rm -rf build', note: 'MODEL PROSE: totally safe, click allow' }, fingerprint: 'f' },
    decision: { verdict: 'ask', rule: 'default:local_write', reason: 'DECISION-REASON', floor: false, approver: { user_id: owner.id, admin_only: false }, timeout: 0, on_timeout: 'deny', choices: ['allow_once', 'deny'] },
    card: {
      title: 'Run shell command',
      risk: 'local_write',
      fields: [
        { label: 'Command', value: 'rm -rf build \u202E/ txt.exe', mono: true },
        { label: 'URL', value: 'https://аpple.example/upload', mono: true },
        { label: 'Note', value: '<img src=x onerror=alert(1)> **not bold**', mono: false },
      ],
      code: 'bash -lc "rm -rf build"',
      diff: '--- a\n+++ b\n-old\n+new',
      warnings: ['First time sending to this recipient'],
      context: 'Telegram · DM · session “Q3 report”',
      choices: ['allow_once', 'allow_session', 'deny'],
      expires: '2099-01-01T00:00:00Z',
      web_url: 'https://bothy.example/approvals/apr_1',
    },
    status: 'pending',
    approver: { user_id: owner.id, admin_only: false },
    on_timeout: 'deny',
    created_at: '2026-10-12T10:00:00Z',
    expires_at: '2099-01-01T00:00:00Z',
    ...extra,
  };
}

describe('ApprovalCard', () => {
  let calls: Call[];

  beforeEach(() => {
    calls = installApi();
    setCsrf('mock-csrf');
    session.me = { user: owner, csrf: 'mock-csrf', mode: 'single', principals: [], paused: false, sandbox_available: true, banners: [] };
    approvals.reset();
  });

  it('renders only the card DTO, never the Action payload or other prose', () => {
    const { container } = render(ApprovalCard, { approval: approval() });
    const text = container.textContent ?? '';
    expect(text).toContain('Run shell command');
    expect(text).toContain('Changes files');
    expect(text).toContain('First time sending to this recipient');
    expect(text).toContain('Telegram · DM · session “Q3 report”');
    expect(text).not.toContain('MODEL PROSE');
    expect(text).not.toContain('DECISION-REASON');
    expect(text).not.toContain('bothy.example/approvals');
    expect(container.querySelector('img')).toBeNull();
    expect(text).toContain('<img src=x onerror=alert(1)> **not bold**');
    expect(container.querySelector('strong')?.textContent).toBe('xn--pple-43d.example');
  });

  it('shows bidi and zero-width characters instead of applying them', () => {
    const { container } = render(ApprovalCard, { approval: approval() });
    expect(container.textContent).toContain('⟨U+202E⟩');
    expect(container.textContent).not.toContain('\u202E');
    expect(screen.getByTitle('Invisible character U+202E')).toBeTruthy();
  });

  it('offers exactly the card choices as native buttons and resolves from the keyboard', async () => {
    render(ApprovalCard, { approval: approval(), shortcuts: true });
    const buttons = screen.getAllByRole('button');
    expect(buttons.map((b) => b.textContent?.replace(/\s+/g, ' ').trim())).toEqual(['Allow once a', 'Allow for this session s', 'Deny d']);
    for (const b of buttons) expect(b.getAttribute('type')).toBe('button');
    expect(buttons[0].getAttribute('aria-keyshortcuts')).toBe('a');

    buttons[0].focus();
    expect(document.activeElement).toBe(buttons[0]);
    await fireEvent.click(buttons[0]);
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Allowed once'));
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.url).toBe('/api/approvals/apr_01jshell00000000000000000');
    expect(JSON.parse(post!.body)).toEqual({ choice: 'allow_once' });
    expect(post?.headers['x-csrf-token']).toBe('mock-csrf');
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('shows no buttons to someone who cannot approve', () => {
    render(ApprovalCard, { approval: approval({ approver: { user_id: 'usr_other', admin_only: false } }) });
    expect(screen.queryByRole('button')).toBeNull();
    expect(screen.getByRole('status').textContent).toContain('Waiting for the owner');
  });

  it('shows the outcome of a decided approval and hides actions in preview mode', () => {
    const { unmount } = render(ApprovalCard, { approval: approval({ status: 'denied', choice: 'deny', decided_by: owner.id, decided_via: 'telegram' }) });
    expect(screen.getByRole('status').textContent).toContain('Denied · by you · via Telegram');
    unmount();
    render(ApprovalCard, { approval: approval(), preview: true });
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('collapses an optional model reason under an unverified label', () => {
    const { container } = render(ApprovalCard, { approval: approval(), reason: 'I need this to build the site.' });
    const details = container.querySelector('details')!;
    expect(details.open).toBe(false);
    expect(details.querySelector('summary')?.textContent).toBe("Model's reason (unverified)");
  });
});
