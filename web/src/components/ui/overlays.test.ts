import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import DialogHarness from './test/DialogHarness.svelte';
import DrawerHarness from './test/DrawerHarness.svelte';
import ToastHost from './ToastHost.svelte';
import { toast, toaster } from './toast.svelte';

const dialog = () => document.querySelector('dialog') as HTMLDialogElement;

async function openDialog(props: Record<string, unknown> = {}) {
  render(DialogHarness, { props });
  const trigger = screen.getByRole('button', { name: 'Open dialog' });
  trigger.focus();
  await fireEvent.click(trigger);
  await waitFor(() => expect(dialog().open).toBe(true));
  return trigger;
}

describe('Dialog', () => {
  it('focuses the first field when it opens', async () => {
    await openDialog();
    expect(document.activeElement).toBe(screen.getByLabelText('Name'));
    expect(document.getElementById(dialog().getAttribute('aria-labelledby')!)?.textContent).toContain('Add person');
  });

  it('focuses Cancel for destructive confirmations', async () => {
    await openDialog({ initialFocus: 'cancel' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }));
  });

  it('closes on Esc and gives focus back to the trigger', async () => {
    const onclose = vi.fn();
    const trigger = await openDialog({ onclose });
    await fireEvent.keyDown(screen.getByLabelText('Name'), { key: 'Escape' });
    expect(dialog().open).toBe(false);
    expect(screen.getByTestId('state').textContent).toBe('closed');
    expect(document.activeElement).toBe(trigger);
    expect(onclose).toHaveBeenCalledOnce();
  });

  it('stays open when it is opened again before the last close event arrives', async () => {
    await openDialog();
    // A browser queues the close event; one that lands after a quick reopen must not close the dialog.
    await fireEvent(dialog(), new Event('close'));
    expect(dialog().open).toBe(true);
    expect(screen.getByTestId('state').textContent).not.toBe('closed');
  });

  it('closes from the footer and from the close button', async () => {
    await openDialog();
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(dialog().open).toBe(false);
    await fireEvent.click(screen.getByRole('button', { name: 'Open dialog' }));
    await waitFor(() => expect(dialog().open).toBe(true));
    await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(dialog().open).toBe(false);
  });

  it('stays open while a request is in flight', async () => {
    await openDialog({ busy: true });
    await fireEvent.keyDown(screen.getByLabelText('Name'), { key: 'Escape' });
    await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    await fireEvent.click(dialog());
    await fireEvent(dialog(), new Event('cancel', { cancelable: true }));
    expect(dialog().open).toBe(true);
    expect(screen.getByRole('button', { name: 'Close' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('closes on a backdrop click only without unsaved input', async () => {
    await openDialog({ dirty: true });
    await fireEvent.click(dialog());
    expect(dialog().open).toBe(true);
    // A click inside the panel is not a backdrop click either.
    await fireEvent.click(screen.getByText('Fill in their details.'));
    expect(dialog().open).toBe(true);
  });

  it('closes on a backdrop click when nothing is typed', async () => {
    await openDialog();
    await fireEvent.click(dialog());
    expect(dialog().open).toBe(false);
  });

  it('keeps Tab inside the dialog', async () => {
    await openDialog();
    const cancel = screen.getByRole('button', { name: 'Cancel' });
    const last = screen.getByRole('button', { name: 'Add person' });
    last.focus();
    await fireEvent.keyDown(last, { key: 'Tab' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close' }));
    await fireEvent.keyDown(document.activeElement!, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);
    expect(cancel.isConnected).toBe(true);
  });
});

describe('Drawer', () => {
  async function openDrawer() {
    render(DrawerHarness);
    const trigger = screen.getByRole('button', { name: 'Open drawer' });
    trigger.focus();
    await fireEvent.click(trigger);
    await waitFor(() => expect(dialog().open).toBe(true));
    return trigger;
  }

  it('focuses its content and traps Tab', async () => {
    await openDrawer();
    const first = screen.getByRole('link', { name: 'First link' });
    const last = screen.getByRole('button', { name: 'Last button' });
    expect(document.activeElement).toBe(first);
    last.focus();
    await fireEvent.keyDown(last, { key: 'Tab' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close' }));
    await fireEvent.keyDown(document.activeElement!, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);
  });

  it('closes on Esc and returns focus', async () => {
    const trigger = await openDrawer();
    await fireEvent.keyDown(screen.getByRole('button', { name: 'Last button' }), { key: 'Escape' });
    expect(dialog().open).toBe(false);
    expect(document.activeElement).toBe(trigger);
    expect(screen.queryByRole('link', { name: 'First link' })).toBeNull();
  });
});

describe('Toast', () => {
  afterEach(() => {
    toaster.dismiss();
    vi.useRealTimers();
  });

  it('shows one toast at a time in a status region', () => {
    render(ToastHost);
    const region = screen.getByRole('status');
    flushSync(() => toast('Saved.'));
    expect(region.textContent).toContain('Saved.');
    flushSync(() => toast('Copied.'));
    expect(region.textContent).toContain('Copied.');
    expect(region.textContent).not.toContain('Saved.');
    expect(region.querySelectorAll('[data-floating]')).toHaveLength(1);
  });

  it('goes away after its duration, pausing while hovered', () => {
    vi.useFakeTimers();
    render(ToastHost);
    flushSync(() => toast('Saved.', { duration: 4000 }));
    const el = screen.getByText('Saved.').closest('[data-floating]')!;
    vi.advanceTimersByTime(3000);
    flushSync(() => el.dispatchEvent(new Event('pointerenter')));
    vi.advanceTimersByTime(10_000);
    expect(screen.queryByText('Saved.')).not.toBeNull();
    flushSync(() => el.dispatchEvent(new Event('pointerleave')));
    vi.advanceTimersByTime(1100);
    flushSync();
    expect(screen.queryByText('Saved.')).toBeNull();
  });

  // "Open chat" vanished after about 4 s, before people reached it.
  it('stays twice as long when it carries an action', () => {
    vi.useFakeTimers();
    render(ToastHost);
    flushSync(() => toast('Saved.'));
    vi.advanceTimersByTime(4100);
    flushSync();
    expect(screen.queryByText('Saved.')).toBeNull();
    flushSync(() => toast('Schedule started.', { action: { label: 'Open chat', onclick: () => {} } }));
    vi.advanceTimersByTime(7000);
    flushSync();
    expect(screen.queryByText('Schedule started.')).not.toBeNull();
    vi.advanceTimersByTime(1100);
    flushSync();
    expect(screen.queryByText('Schedule started.')).toBeNull();
  });

  it('runs its action once and closes', async () => {
    render(ToastHost);
    const undo = vi.fn();
    flushSync(() => toast('Chat deleted.', { action: { label: 'Undo', onclick: undo } }));
    await fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
    expect(undo).toHaveBeenCalledOnce();
    expect(screen.queryByText('Chat deleted.')).toBeNull();
  });

  it('never shows the same toast twice when two hosts are mounted', () => {
    render(ToastHost);
    render(ToastHost);
    flushSync(() => toast('Saved.'));
    expect(screen.getAllByText('Saved.')).toHaveLength(1);
  });
});
