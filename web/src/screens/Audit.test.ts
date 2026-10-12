import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { router } from '../lib/router.svelte';
import { session } from '../lib/stores/session.svelte';
import { installApi } from '../test/api';
import Audit from './Audit.svelte';

describe('Audit filters', () => {
  it('queries on Filter, not on every keystroke, and keeps the filter in the URL', async () => {
    const calls = installApi();
    await session.init();
    router.navigate('/audit');
    render(Audit);
    const audits = () => calls.filter((c) => c.url.startsWith('/api/audit?'));
    await waitFor(() => expect(audits()).toHaveLength(1));

    const prefix = screen.getByRole('combobox', { name: 'Action prefix' });
    await fireEvent.input(prefix, { target: { value: 'v' } });
    await fireEvent.input(prefix, { target: { value: 'vault.' } });
    await new Promise((r) => setTimeout(r, 20));
    expect(audits()).toHaveLength(1);

    await fireEvent.click(screen.getByRole('button', { name: 'Filter' }));
    await waitFor(() => expect(audits()).toHaveLength(2));
    expect(audits()[1].url).toContain('action=vault.');
    expect(router.query.get('action')).toBe('vault.');
  });
});
