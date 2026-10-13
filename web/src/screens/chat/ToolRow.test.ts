import { describe, expect, it } from 'vitest';
import { installApi } from '../../test/api';
import { runDetail } from './ToolRow.svelte';

describe('ToolRow run cache', () => {
  it('shares one fetch per run, forgets failures and keeps only recent runs', async () => {
    let fail = true;
    const calls = installApi({
      override: (m, url) => {
        if (!url.startsWith('/api/runs/')) return undefined;
        if (url.endsWith('/run_flaky') && fail) return new Response('{"error":{"code":"unavailable","message":"down"}}', { status: 503 });
        return { run: null, llm_calls: [], tool_calls: [], approvals: [] };
      },
    });
    const runCalls = (id: string) => calls.filter((c) => c.url === `/api/runs/${id}`).length;

    await Promise.all([runDetail('run_a'), runDetail('run_a')]);
    expect(runCalls('run_a')).toBe(1);

    await expect(runDetail('run_flaky')).rejects.toThrow('down');
    fail = false;
    await runDetail('run_flaky');
    expect(runCalls('run_flaky')).toBe(2);

    for (let i = 0; i < 20; i++) await runDetail(`run_${i}`);
    await runDetail('run_a');
    expect(runCalls('run_a')).toBe(2);
  });
});
