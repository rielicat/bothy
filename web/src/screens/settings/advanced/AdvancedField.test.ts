import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import AdvancedField from '../../../components/AdvancedField.svelte';
import { config } from '../../../lib/config.svelte';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';

// The mock seeds sandbox.limits.memory_mb as a UI change (1024 over the
// default 512), log.level from BOTHY_LOG__LEVEL and sandbox.socket locked.
describe('AdvancedField', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    await session.init();
    config.view = null;
    await config.load(true);
    calls.length = 0;
  });

  it('shows "Changed · Reset to default" only when the value differs from the default', async () => {
    render(AdvancedField, { path: 'sandbox.limits.cpus', label: 'CPUs', type: 'number' });
    render(AdvancedField, { path: 'sandbox.limits.memory_mb', label: 'Memory', type: 'number' });
    const memory = screen.getByLabelText('Memory');
    expect(screen.getAllByText(/Changed ·/)).toHaveLength(1);
    expect(memory.closest('.grid')?.textContent).toContain('Changed');
    expect(screen.getByLabelText('CPUs').closest('.grid')?.textContent).not.toContain('Changed');
  });

  it('resets a changed value and the note goes away', async () => {
    render(AdvancedField, { path: 'sandbox.limits.memory_mb', label: 'Memory', type: 'number' });
    await fireEvent.click(screen.getByRole('button', { name: 'Reset to default' }));
    await waitFor(() => expect(screen.queryByText(/Changed ·/)).toBeNull());
    expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/config/sandbox.limits.memory_mb')).toBe(true);
    expect((screen.getByLabelText('Memory') as HTMLInputElement).value).toBe('512');
  });

  it('makes a value from the server environment read-only and names the variable', () => {
    render(AdvancedField, {
      path: 'log.level',
      label: 'Log level',
      type: 'select',
      options: [
        { value: 'debug', label: 'Debug' },
        { value: 'info', label: 'Info' },
      ],
    });
    expect(screen.getByText('Set by the server environment (BOTHY_LOG__LEVEL). Change it there.')).toBeTruthy();
    expect((screen.getByLabelText('Log level') as HTMLSelectElement).disabled).toBe(true);
    expect(screen.queryByText(/Changed ·/)).toBeNull();
  });

  it('makes a locked value read-only', () => {
    render(AdvancedField, { path: 'sandbox.socket', label: 'Socket' });
    expect(screen.getByText("Locked on this server, so it can't be changed here.")).toBeTruthy();
    expect((screen.getByLabelText('Socket') as HTMLInputElement).readOnly).toBe(true);
  });

  it('saves text on blur and says so', async () => {
    render(AdvancedField, { path: 'sandbox.limits.cpus', label: 'CPUs', type: 'number' });
    const input = screen.getByLabelText('CPUs') as HTMLInputElement;
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: '2' } });
    await fireEvent.blur(input);
    await waitFor(() => expect(screen.getByText('Saved')).toBeTruthy());
    expect(calls.find((c) => c.method === 'PATCH')?.body).toBe('{"set":{"sandbox.limits.cpus":2}}');
    expect(screen.getByText(/Changed ·/)).toBeTruthy();
  });

  it('saves on Enter and checks what was typed first', async () => {
    render(AdvancedField, { path: 'sandbox.limits.pids', label: 'Processes', type: 'number' });
    const input = screen.getByLabelText('Processes') as HTMLInputElement;
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: '2.5' } });
    await fireEvent.keyDown(input, { key: 'Enter' });
    expect(screen.getByText('Enter a whole number.')).toBeTruthy();
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('parses durations and lists', async () => {
    render(AdvancedField, { path: 'sandbox.idle_timeout', label: 'Idle', type: 'duration' });
    render(AdvancedField, { path: 'egress.ports', label: 'Ports', type: 'list' });
    const idle = screen.getByLabelText('Idle') as HTMLInputElement;
    expect(idle.value).toBe('10m');
    await fireEvent.focus(idle);
    await fireEvent.input(idle, { target: { value: 'soon' } });
    await fireEvent.blur(idle);
    expect(screen.getByText('Enter a duration like 45s, 10m or 2h.')).toBeTruthy();
    const ports = screen.getByLabelText('Ports') as HTMLInputElement;
    await fireEvent.focus(ports);
    await fireEvent.input(ports, { target: { value: '443, 8443' } });
    await fireEvent.blur(ports);
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH')?.body).toBe('{"set":{"egress.ports":[443,8443]}}'));
  });

  it('says when a change needs a restart', () => {
    render(AdvancedField, { path: 'sandbox.backend', label: 'Backend', type: 'select', options: [{ value: 'auto', label: 'Automatic' }] });
    expect(screen.getByText('Takes effect after a restart.')).toBeTruthy();
  });

  it('treats the empty choice as no value of its own', async () => {
    const options = [
      { value: '', label: 'As the approval setting says' },
      { value: 'ask', label: 'Ask' },
    ];
    render(AdvancedField, { path: 'policy.defaults.read', label: 'Reading', type: 'select', options });
    const select = screen.getByLabelText('Reading') as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'ask' } });
    await waitFor(() => expect(screen.getByText(/Changed ·/)).toBeTruthy());
    await fireEvent.change(select, { target: { value: '' } });
    await waitFor(() => expect(screen.queryByText(/Changed ·/)).toBeNull());
    expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/config/policy.defaults.read')).toBe(true);
  });

  it('shows nothing for a setting this server does not have', () => {
    const { container } = render(AdvancedField, { path: 'agent.not_a_setting', label: 'Not a setting', type: 'number' });
    expect(container.textContent?.trim()).toBe('');
  });
});
