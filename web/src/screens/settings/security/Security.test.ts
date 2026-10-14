import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../../mock/server';
import { toaster } from '../../../components/ui';
import { config } from '../../../lib/config.svelte';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';
import Security from './Security.svelte';

async function open(mock = ''): Promise<Call[]> {
  const calls = installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  config.view = null;
  render(Security);
  await screen.findByText('When should Bothy ask?');
  return calls;
}

describe('Security', () => {
  beforeEach(() => toaster.dismiss());

  it('lists what needs attention in plain words, worst first, without notes', async () => {
    await open();
    const section = (await screen.findByRole('heading', { name: 'Needs attention' })).closest('section')!;
    await waitFor(() => expect(within(section).getAllByText(/\.$/).map((p) => p.textContent)).toEqual([
      'Data files can be read by other users on this server.',
      'The key that protects saved keys is stored next to the database.',
    ]));
    expect(within(section).getByRole('button', { name: 'Fix' })).toBeTruthy();
    expect(screen.queryByText(/vault/i)).toBeNull();
  });

  it('fixes a finding and drops it', async () => {
    const calls = await open();
    await fireEvent.click(await screen.findByRole('button', { name: 'Fix' }));
    await waitFor(() => expect(screen.queryByText('Data files can be read by other users on this server.')).toBeNull());
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/security-audit' && c.body === '{"fix":true}')).toBe(true);
    expect(toaster.current?.text).toBe('Fixed.');
    // The fixed row is gone; focus lands on the next finding's action, not the body.
    await waitFor(() => expect(document.activeElement?.textContent?.trim()).toBe('How to fix'));
  });

  it('opens plain steps behind How to fix', async () => {
    await open();
    const how = await screen.findByRole('button', { name: 'How to fix' });
    expect(how.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(how);
    expect(how.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getByText(/Keep the key on its own volume/)).toBeTruthy();
  });

  it('has no Needs attention section when nothing is wrong', async () => {
    await open('no-findings');
    await waitFor(() => expect(screen.getByText('On. Code runs in an isolated workspace.')).toBeTruthy());
    expect(screen.queryByRole('heading', { name: 'Needs attention' })).toBeNull();
  });

  it('saves the approval preset on change and says Saved', async () => {
    const calls = await open();
    expect((screen.getByRole('radio', { name: 'Balanced' }) as HTMLInputElement).checked).toBe(true);
    await fireEvent.click(screen.getByRole('radio', { name: 'Careful' }));
    await waitFor(() => expect(screen.getByText('Saved')).toBeTruthy());
    expect(calls.find((c) => c.method === 'PATCH')?.body).toBe('{"set":{"policy.preset":"careful"}}');
    // Shown for 2 s, then the line is clear again (§11.1).
    await waitFor(() => expect(screen.queryByText('Saved')).toBeNull(), { timeout: 3000 });
  });

  it('writes the timeout and the outcome together', async () => {
    const calls = await open();
    const select = screen.getByLabelText('If nobody answers a chat request') as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toEqual(['Deny after 15 minutes', 'Deny after 1 hour', 'Keep waiting (up to 24 hours)']);
    await fireEvent.change(select, { target: { value: 'deny-1h' } });
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set).toEqual({
      'policy.approval.interactive_timeout': '1h',
      'policy.approval.interactive_on_timeout': 'deny',
    });
    expect([...(screen.getByLabelText('If nobody answers a scheduled request') as HTMLSelectElement).options].map((o) => o.text)).toEqual([
      'Wait up to 24 hours',
      'Deny after 1 hour',
    ]);
  });

  it('turns package downloads on from a switch', async () => {
    const calls = await open();
    const sw = screen.getByRole('switch', { name: 'Python packages (PyPI)' });
    await fireEvent.click(sw);
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH')?.body).toBe('{"set":{"egress.presets.python_packages":true}}'));
  });

  it('counts permissions, sites and saved keys, linking each page', async () => {
    await open();
    await waitFor(() => expect(screen.getByRole('link', { name: /Allowed without asking\s*4 permissions/ })).toBeTruthy());
    expect(screen.getByRole('link', { name: /Websites Bothy can reach\s*4 sites/ }).getAttribute('href')).toBe('/settings/security/websites');
    await waitFor(() => expect(screen.getByRole('link', { name: /Saved keys\s*6 saved keys/ }).getAttribute('href')).toBe('/settings/security/keys'));
  });

  it('explains how to turn running code on, with the commands under Details', async () => {
    await open('sandbox-off');
    await waitFor(() => expect(screen.getAllByRole('button', { name: 'How to turn it on' })).toHaveLength(2));
    const how = screen.getAllByRole('button', { name: 'How to turn it on' });
    // One in Needs attention moves to the one under Running code and opens it.
    await fireEvent.click(how[0]);
    const toggle = how[how.length - 1];
    await waitFor(() => expect(toggle.getAttribute('aria-expanded')).toBe('true'));
    expect(document.activeElement).toBe(toggle);
    expect(screen.getByText(/Start Bothy with the compose file from the deployment guide/)).toBeTruthy();
    await fireEvent.click(screen.getAllByRole('button', { name: 'Details' }).at(-1)!);
    expect(screen.getByText(/docker compose up -d/)).toBeTruthy();
  });

  it('pauses from a dialog that starts on Cancel, then resumes', async () => {
    const calls = await open();
    const trigger = screen.getByRole('button', { name: 'Pause Bothy…' });
    trigger.focus();
    await fireEvent.click(trigger);
    const dialog = await screen.findByRole('dialog', { name: 'Pause Bothy?' });
    expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(within(dialog).getByText(/Approvals that are waiting will be cancelled\./)).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Pause Bothy' }));
    await screen.findByRole('button', { name: 'Resume' });
    expect(calls.find((c) => c.url === '/api/admin/pause')?.body).toBe('{"paused":true}');
    expect(screen.getByText(/^Bothy has been paused since .+ by Sam\.$/)).toBeTruthy();
    expect(toaster.current?.text).toBe('Bothy paused.');
    await fireEvent.click(screen.getByRole('button', { name: 'Resume' }));
    await screen.findByRole('button', { name: 'Pause Bothy…' });
    expect(calls.filter((c) => c.url === '/api/admin/pause').at(-1)?.body).toBe('{"paused":false}');
  });

  it('shows the paused state it finds on load', async () => {
    await open('paused');
    expect(screen.getByText('Bothy is paused. Nothing new starts until someone resumes it.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Resume' })).toBeTruthy();
  });

  it('tells members the page is for admins', async () => {
    installApi();
    resetMock({ mock: 'member' });
    session.ready = false;
    await session.init();
    render(Security);
    expect(screen.getByText('Only admins can see this page.')).toBeTruthy();
    expect(screen.queryByText('When should Bothy ask?')).toBeNull();
  });
});
