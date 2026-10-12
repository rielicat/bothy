import { render, waitFor } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { config } from '../../lib/config.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import StepModels from './StepModels.svelte';

describe('wizard models step', () => {
  it('probes for local models once, not after every config change', async () => {
    const calls = installApi();
    await session.init();
    render(StepModels, { onnext: () => {} });
    const detects = () => calls.filter((c) => c.url === '/api/providers/detect');
    await waitFor(() => expect(detects()).toHaveLength(1));
    await waitFor(() => expect(config.view).not.toBeNull());
    await config.set({ 'models.fast': ['anthropic/claude-haiku-4-5'] }, true);
    await new Promise((r) => setTimeout(r, 20));
    expect(detects()).toHaveLength(1);
  });
});
