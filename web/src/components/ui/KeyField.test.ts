import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import KeyField, { type CheckResult } from './KeyField.svelte';

const KEY = 'sk-ant-api03-0123456789abcdefghij';

function setup(check: (v: string, s: AbortSignal) => Promise<CheckResult>, props: Record<string, unknown> = {}) {
  render(KeyField, { props: { label: 'Anthropic API key', check, ...props } });
  return screen.getByLabelText('Anthropic API key') as HTMLInputElement;
}

const type = (input: HTMLInputElement, value: string) => fireEvent.input(input, { target: { value } });

describe('KeyField', () => {
  afterEach(() => vi.useRealTimers());

  it('checks right away when a key is pasted', async () => {
    const check = vi.fn(async (_v: string, _s: AbortSignal): Promise<CheckResult> => ({ ok: true, message: 'Connected. Bothy will use Claude Sonnet 4.5.' }));
    const input = setup(check);
    await fireEvent.paste(input);
    await type(input, KEY);
    expect(check).toHaveBeenCalledOnce();
    expect(check.mock.calls[0][0]).toBe(KEY);
    await screen.findByText('Connected. Bothy will use Claude Sonnet 4.5.');
    expect(screen.getByRole('status').textContent).toContain('Connected');
  });

  it('waits 800 ms after typing stops, and only for a long enough value', async () => {
    vi.useFakeTimers();
    const check = vi.fn(async (_v: string, _s: AbortSignal): Promise<CheckResult> => ({ ok: true, message: 'Connected.' }));
    const input = setup(check, { minLength: 20 });
    await type(input, 'sk-ant');
    vi.advanceTimersByTime(2000);
    expect(check).not.toHaveBeenCalled();
    await type(input, KEY.slice(0, -1));
    vi.advanceTimersByTime(500);
    await type(input, KEY);
    vi.advanceTimersByTime(799);
    expect(check).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(check).toHaveBeenCalledOnce();
    expect(check.mock.calls[0][0]).toBe(KEY);
  });

  it('checks on Enter', async () => {
    const check = vi.fn(async (_v: string, _s: AbortSignal): Promise<CheckResult> => ({ ok: true, message: 'Connected.' }));
    const input = setup(check);
    await type(input, 'short');
    await fireEvent.keyDown(input, { key: 'Enter' });
    expect(check).toHaveBeenCalledWith('short', expect.any(AbortSignal));
  });

  it('aborts the pending check when the value changes and ignores its answer', async () => {
    const pending: { signal: AbortSignal; resolve: (r: CheckResult) => void }[] = [];
    const check = vi.fn((_v: string, signal: AbortSignal) => new Promise<CheckResult>((resolve) => pending.push({ signal, resolve })));
    const input = setup(check);
    await fireEvent.paste(input);
    await type(input, KEY);
    expect(screen.getByText('Checking…')).toBeTruthy();
    await type(input, KEY + 'x');
    expect(pending[0].signal.aborted).toBe(true);
    pending[0].resolve({ ok: true, message: 'Connected with the old key.' });
    await fireEvent.paste(input);
    await type(input, KEY + 'y');
    expect(pending[1].signal.aborted).toBe(false);
    pending[1].resolve({ ok: false, message: "Anthropic didn't accept this key.", details: '401 invalid x-api-key' });
    await screen.findByText("Anthropic didn't accept this key.");
    expect(screen.queryByText('Connected with the old key.')).toBeNull();
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(screen.getByRole('alert').textContent).toContain("Anthropic didn't accept this key.");
    // The raw message waits behind Details.
    expect(screen.queryByText('401 invalid x-api-key')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Details' }));
    expect(screen.getByText('401 invalid x-api-key')).toBeTruthy();
  });

  it('turns a thrown check into a plain failure', async () => {
    const input = setup(async () => {
      throw new Error('network down');
    });
    await fireEvent.paste(input);
    await type(input, KEY);
    await screen.findByText("Bothy couldn't check this key. Try again.");
  });

  it('shows a stored key as saved, with Replace', async () => {
    render(KeyField, { props: { label: 'OpenAI API key', check: vi.fn(), saved: true } });
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.getByText(/saved/).textContent).toContain('•••••');
    await fireEvent.click(screen.getByRole('button', { name: 'Replace' }));
    const input = await waitFor(() => screen.getByLabelText('OpenAI API key') as HTMLInputElement);
    flushSync();
    expect(input.value).toBe('');
  });

  it('masks the key until Show is pressed', async () => {
    const input = setup(vi.fn());
    expect(input.type).toBe('password');
    await fireEvent.click(screen.getByRole('button', { name: 'Show' }));
    expect(input.type).toBe('text');
  });
});
