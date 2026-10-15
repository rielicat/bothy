import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ModelDialog from './ModelDialog.svelte';

const options = Array.from({ length: 20 }, (_, i) => ({ value: `or/m${i}`, label: i === 7 ? 'Claude Sonnet 5.5' : `Model ${i}`, group: 'OpenRouter' }));

describe('ModelDialog', () => {
  it('searches a long list and saves the match with Enter', async () => {
    const onsave = vi.fn(async () => true);
    render(ModelDialog, { open: true, title: 'Main model', value: 'or/m0', options, onsave });
    const dialog = await screen.findByRole('dialog', { name: 'Main model' });
    const search = within(dialog).getByLabelText('Search models');
    await fireEvent.input(search, { target: { value: 'nothing like this' } });
    expect(within(dialog).getByText('No model matches “nothing like this”.')).toBeTruthy();
    await fireEvent.input(search, { target: { value: 'sonnet' } });
    expect(within(dialog).getAllByRole('option').map((o) => o.textContent?.trim())).toEqual(['Claude Sonnet 5.5']);
    await fireEvent.keyDown(search, { key: 'Enter' });
    expect(onsave).toHaveBeenCalledWith('or/m7');
  });

  it('shows no search field for a short list', async () => {
    render(ModelDialog, { open: true, title: 'Main model', value: 'or/m0', options: options.slice(0, 5), onsave: vi.fn(async () => true) });
    const dialog = await screen.findByRole('dialog', { name: 'Main model' });
    expect(within(dialog).queryByLabelText('Search models')).toBeNull();
  });
});
