import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

const renders = vi.hoisted(() => ({ n: 0 }));
vi.mock('../../lib/markdown', async (orig) => {
  const m = await orig<typeof import('../../lib/markdown')>();
  return { ...m, renderMarkdown: (...a: Parameters<typeof m.renderMarkdown>) => (renders.n++, m.renderMarkdown(...a)) };
});

import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import ConversationView from './ConversationView.svelte';

describe('streaming', () => {
  // Deltas arrive up to 20 times a second; re-rendering every earlier message
  // on each one made long chats burn CPU while a reply streamed.
  it('renders only the streaming text on each delta', async () => {
    installApi();
    await session.init();
    const sid = 'ses_01jtrip000000000000000000';
    render(ConversationView, { id: sid });
    await screen.findByText(/Here's a simple plan/);
    await new Promise((r) => setTimeout(r, 20));
    const before = renders.n;
    for (let i = 0; i < 5; i++) {
      FakeEventSource.last.emit('run.delta', { seq: 1000 + i, type: 'run.delta', principal_id: 'usr_x', session_id: sid, run_id: 'run_x', at: '', data: { text: 'hi ' } });
      await new Promise((r) => setTimeout(r, 5));
    }
    expect(renders.n - before).toBe(5);
  });
});
