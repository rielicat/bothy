import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { approvals, session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import ProjectNotes from './ProjectNotes.svelte';

const params = { owner: 'acme', repo: 'web' };

async function start(mock = '') {
  const calls = installApi();
  if (mock) resetMock({ mock });
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
    expect(await screen.findByText('Saved.')).toBeTruthy();
    expect(screen.queryByLabelText('Notes for acme/web')).toBeNull();
    expect(screen.getByText('Keep pull requests small.')).toBeTruthy();
  });

  it('on an If-Match conflict, keeps the edit and saves over their version only when asked', async () => {
    const calls = await start('notes-conflict');
    await editAndSave('- Keep pull requests small.');
    expect(await screen.findByText('Someone else changed these notes while you were editing.')).toBeTruthy();
    const first = puts(calls)[0].headers['if-match'];
    expect((screen.getByLabelText('Notes for acme/web') as HTMLTextAreaElement).value).toContain('Keep pull requests small.');

    await fireEvent.click(screen.getByRole('button', { name: 'See their version' }));
    const dialog = screen.getByRole('dialog', { name: 'Their version and yours' });
    expect(within(dialog).getByText(/shared date helpers/)).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Keep mine' }));

    await waitFor(() => expect(puts(calls)).toHaveLength(2));
    const second = puts(calls)[1].headers['if-match'];
    expect(second).not.toBe(first);
    expect(await screen.findByText('Saved.')).toBeTruthy();
  });

  it('can take their version instead', async () => {
    const calls = await start('notes-conflict');
    await editAndSave('- Mine.');
    await screen.findByText('Someone else changed these notes while you were editing.');
    await fireEvent.click(screen.getByRole('button', { name: 'See their version' }));
    await fireEvent.click(within(screen.getByRole('dialog', { name: 'Their version and yours' })).getByRole('button', { name: 'Use theirs' }));
    await waitFor(() => expect(screen.queryByLabelText('Notes for acme/web')).toBeNull());
    expect(screen.getByText(/shared date helpers/)).toBeTruthy();
    expect(screen.queryByText('Mine.')).toBeNull();
    expect(puts(calls)).toHaveLength(1);
  });

  it('refuses to send notes over the size limit', async () => {
    const calls = await start('notes-near-full');
    expect(screen.getByText('These notes are almost full. Shorten them so Bothy has room to add more.')).toBeTruthy();
    await editAndSave('More detail. '.repeat(40));
    expect(await screen.findByText('These notes are too long to save. Remove something first.')).toBeTruthy();
    expect(puts(calls)).toHaveLength(0);
  });

  it('asks before throwing edits away on Escape', async () => {
    await start();
    await fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const box = screen.getByLabelText('Notes for acme/web');
    await fireEvent.input(box, { target: { value: 'changed' } });
    await fireEvent.keyDown(box, { key: 'Escape' });
    const dialog = screen.getByRole('dialog', { name: 'Discard your changes?' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Discard' }));
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

  it('gives members no admin actions', async () => {
    await start('member');
    await fireEvent.click(screen.getByRole('button', { name: 'Project options' }));
    const items = screen.getAllByRole('menuitem').map((m) => m.textContent?.trim());
    expect(items).toEqual(['Ask Bothy about this project', 'View on GitHub ↗', 'Technical details']);
    expect(screen.getByRole('button', { name: 'Edit' })).toBeTruthy();
  });
});
