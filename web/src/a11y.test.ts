import { render, screen, waitFor, within } from '@testing-library/svelte';
import axe from 'axe-core';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import App from './App.svelte';
import { router } from './lib/router.svelte';
import { session } from './lib/stores/session.svelte';
import { shell } from './lib/stores/ui.svelte';
import { installApi } from './test/api';

// jsdom has no layout or computed colours, so contrast is checked in the
// browser smoke test (e2e/ui.spec.ts) instead.
const options: axe.RunOptions = { rules: { 'color-contrast': { enabled: false } } };

async function check(path: string, ready: () => HTMLElement | Promise<HTMLElement>) {
  router.navigate(path);
  render(App);
  await waitFor(ready, { timeout: 3000 });
  await new Promise((r) => setTimeout(r, 50));
  const result = await axe.run(document.body, options);
  const summary = result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  expect(summary, path).toEqual([]);
}

describe('accessibility (axe)', () => {
  beforeEach(async () => {
    installApi();
    session.ready = false;
    await session.init();
  });

  it('chat', () => check('/chat/ses_01jtrip000000000000000000', () => screen.getByText(/Here's a simple plan/)));
  // v0.2 lists chats in the shell sidebar (C1); /chat is the new chat itself (docs/design/ui.md §7.2).
  it('new chat', () => check('/chat', () => screen.getByText('What should Bothy work on?')));
  it('approvals', () => check('/approvals', () => screen.getByRole('heading', { name: 'Waiting for others' })));
  it('approvals, done', () => check('/approvals?tab=done', () => screen.getByRole('heading', { name: 'Today' })));
  it('settings: general', () => check('/settings/general', () => screen.getByRole('heading', { name: 'Workspace' })));
  it('settings: models', () => check('/settings/models', () => screen.getByText('Main model')));
  it('settings: security, the old policy address', () => check('/settings/policy', () => screen.getByText('When should Bothy ask?')));
  // v0.2 folds budgets into General (docs/design/ui.md §4.5), so the old address lands there.
  it('settings: budgets, now part of General', () => check('/settings/budgets', () => screen.getByRole('heading', { name: 'Workspace' })));
  it('settings: connections', () => check('/settings/channels', () => screen.getByText('Connected to Acme · 2 people')));
  it('settings: saved keys, the old vault address', () => check('/settings/vault', () => screen.getByText('Anthropic API key')));
  it('schedules', () => check('/schedules', () => screen.getByText('Morning briefing')));
  it('memory', () => check('/memory', () => screen.getByText('History')));
});

describe('accessibility (axe), first run', () => {
  beforeEach(async () => {
    installApi({ setup: true });
    session.ready = false;
    session.me = null;
    await session.init();
  });

  it('setup wizard', () => check('/setup', () => screen.getByText('Set up Bothy')));
});

describe('accessibility (axe), related chats in the sidebar', () => {
  beforeEach(async () => {
    installApi({ mock: 'team-chats' } as Parameters<typeof installApi>[0]);
    session.ready = false;
    session.me = null;
    await session.init();
  });
  afterEach(() => {
    shell.drawer = false;
    delete (window as { matchMedia?: unknown }).matchMedia;
  });

  // The open chat's family opens by itself, so a closed family and an open one are both on screen.
  const family = () => screen.getAllByRole('list', { name: 'Related to “Q3 pricing notes”' })[0];

  it('wide (1280)', () => check('/chat/ses_01jteamcopy0000000000000000', family));

  it('in the phone drawer (360)', () =>
    check('/chat/ses_01jteamcopy0000000000000000', async () => {
      window.matchMedia = ((q: string) => ({ matches: q.includes('max-width') || q.includes('coarse'), media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
      shell.drawer = true;
      const drawer = await screen.findByRole('dialog', { name: 'Menu' });
      return within(drawer).getByRole('list', { name: 'Related to “Q3 pricing notes”' });
    }));
});
