import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { toaster } from '../../components/ui';
import { approvals, session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import ProjectNotes from './ProjectNotes.svelte';

const params = { owner: 'acme', repo: 'web' };

async function start(mock = '') {
  const calls = installApi();
  if (mock) resetMock({ mock });
  toaster.dismiss();
  approvals.reset();
  session.ready = false;
  await session.init();
  render(ProjectNotes, { params });
  await screen.findByText('How to build and test');
  return calls;
}

const puts = (calls: Call[]) => calls.filter((c) => c.method === 'PUT' && c.url === '/api/knowledge/repos/1/notes');

async function editAndSave(text: string) {
  await fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
  const box = screen.getByLabelText('Notes for acme/web') as HTMLTextAreaElement;
  await fireEvent.input(box, { target: { value: `${box.value}\n${text}` } });
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
}

describe('Project notes', () => {
  beforeEach(() => {
    approvals.reset();
  });

  it('saves with If-Match and shows the result', async () => {
    const calls = await start();
    await editAndSave('- Keep pull requests small.');
    await waitFor(() => expect(puts(calls)).toHaveLength(1));
    expect(puts(calls)[0].headers['if-match']).toMatch(/^"n[0-9a-f]+"$/);
    await waitFor(() => expect(toaster.current?.text).toBe('Saved.'));
    expect(screen.queryByLabelText('Notes for acme/web')).toBeNull();
    expect(screen.getByText('Keep pull requests small.')).toBeTruthy();
  });

  it('on an If-Match conflict, keeps the edit and saves over their version only when asked', async () => {
    const calls = await start('notes-conflict');
    await editAndSave('- Keep pull requests small.');
    expect(await screen.findByText('Someone else changed these notes while you were editing.')).toBeTruthy();
    const first = puts(calls)[0].headers['if-match'];
    expect((screen.getByLabelText('Notes for acme/web') as HTMLTextAreaElement).value).toContain('Keep pull requests small.');

    // Saving again would only meet the conflict again, so the primary compares instead.
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Compare versions' }));
    const dialog = screen.getByRole('dialog', { name: 'Compare versions' });
    expect(within(dialog).getByText(/shared date helpers/)).toBeTruthy();
    // Cancel only closes; nothing is replaced or saved.
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(puts(calls)).toHaveLength(1);
    expect((screen.getByLabelText('Notes for acme/web') as HTMLTextAreaElement).value).toContain('Keep pull requests small.');
    await fireEvent.click(screen.getByRole('button', { name: 'Compare versions' }));
    await fireEvent.click(within(screen.getByRole('dialog', { name: 'Compare versions' })).getByRole('button', { name: 'Keep mine' }));

    await waitFor(() => expect(puts(calls)).toHaveLength(2));
    const second = puts(calls)[1].headers['if-match'];
    expect(second).not.toBe(first);
    await waitFor(() => expect(toaster.current?.text).toBe('Saved.'));
  });

  it('leaves ⌘S / Ctrl S to the person while they compare versions', async () => {
    const calls = await start('notes-conflict');
    await editAndSave('- Mine.');
    await screen.findByText('Someone else changed these notes while you were editing.');
    await fireEvent.click(screen.getByRole('button', { name: 'Compare versions' }));
    const dialog = screen.getByRole('dialog', { name: 'Compare versions' });
    await fireEvent.keyDown(within(dialog).getByRole('button', { name: 'Use their version' }), { key: 's', ctrlKey: true });
    expect(puts(calls)).toHaveLength(1);
  });

  it('can take their version instead', async () => {
    const calls = await start('notes-conflict');
    await editAndSave('- Mine.');
    await screen.findByText('Someone else changed these notes while you were editing.');
    await fireEvent.click(screen.getByRole('button', { name: 'Compare versions' }));
    await fireEvent.click(within(screen.getByRole('dialog', { name: 'Compare versions' })).getByRole('button', { name: 'Use their version' }));
    await waitFor(() => expect(screen.queryByLabelText('Notes for acme/web')).toBeNull());
    expect(screen.getByText(/shared date helpers/)).toBeTruthy();
    expect(screen.queryByText('Mine.')).toBeNull();
    expect(puts(calls)).toHaveLength(1);
  });

  it('refuses to send notes over the size limit', async () => {
    const calls = await start('notes-near-full');
    expect(screen.getByText('These notes are almost full. Shorten them so Bothy has room to add more.')).toBeTruthy();
    await editAndSave('More detail. '.repeat(40));
    // Said once, next to Save rather than under a long document, and the field points at it.
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toBe('These notes are too long to save. Remove something first.');
    expect(screen.getAllByText('These notes are too long to save. Remove something first.')).toHaveLength(1);
    expect(screen.getByLabelText('Notes for acme/web').getAttribute('aria-describedby')).toBe(alert.id);
    expect(screen.getByLabelText('Notes for acme/web').getAttribute('aria-invalid')).toBe('true');
    expect(puts(calls)).toHaveLength(0);
  });

  it('offers no actions for a repository Bothy does not read', async () => {
    installApi();
    approvals.reset();
    session.ready = false;
    await session.init();
    render(ProjectNotes, { params: { owner: 'acme', repo: 'unknown' } });
    await screen.findByText("Bothy doesn't read this repository.");
    expect(screen.queryByRole('button', { name: 'Project options' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
  });

  it('asks before throwing edits away on Escape', async () => {
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const box = screen.getByLabelText('Notes for acme/web');
    await fireEvent.input(box, { target: { value: 'changed' } });
    await fireEvent.keyDown(box, { key: 'Escape' });
    const dialog = screen.getByRole('dialog', { name: 'Discard your changes?' });
    expect(within(dialog).getByText("This can't be undone.")).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(screen.queryByLabelText('Notes for acme/web')).toBeNull());
    expect(screen.getByText('How to build and test')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Edit' })));
  });

  it('shows Bothy’s suggestions and closes the review once the last one is decided', async () => {
    await start('notes-suggestions');
    await screen.findByText('Bothy suggested 2 changes to these notes.');
    await fireEvent.click(screen.getByRole('button', { name: 'Review' }));
    const dialog = screen.getByRole('dialog', { name: 'Suggested changes' });
    expect(within(dialog).getAllByRole('article')).toHaveLength(2);
    // Deciding elsewhere (the inbox, a chat app) updates this page too.
    approvals.pending = approvals.pending.filter((a) => a.action?.tool !== 'notes');
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Suggested changes' })).toBeNull());
    expect(screen.queryByText(/Bothy suggested/)).toBeNull();
  });

  it('says stopping deletes the notes for good before it does', async () => {
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'Project options' }));
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Stop reading this repository…' }));
    const dialog = screen.getByRole('dialog', { name: 'Stop reading acme/web?' });
    expect(dialog.textContent).toContain("Bothy won't use acme/web to answer questions, and its project notes and their history will be deleted. This can't be undone.");
  });

  it('gives members no admin actions', async () => {
    await start('member');
    await fireEvent.click(screen.getByRole('button', { name: 'Project options' }));
    const items = screen.getAllByRole('menuitem').map((m) => m.textContent?.trim());
    expect(items).toEqual(['Ask Bothy about this project', 'View on GitHub ↗', 'Technical details']);
    expect(screen.getByRole('button', { name: 'Edit' })).toBeTruthy();
  });
});
