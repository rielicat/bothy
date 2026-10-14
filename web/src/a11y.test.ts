import { render, screen, waitFor } from '@testing-library/svelte';
import axe from 'axe-core';
import { beforeEach, describe, expect, it } from 'vitest';
import App from './App.svelte';
import { router } from './lib/router.svelte';
import { session } from './lib/stores/session.svelte';
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

  it('chat', () => check('/chat/ses_01jtrip000000000000000000', () => screen.getByText(/Here's a gentle plan/)));
  it('chat list', () => check('/chat', () => screen.getByText('Weekend in Edinburgh')));
  it('approvals', () => check('/approvals', () => screen.getByRole('heading', { name: 'Waiting for others' })));
  it('approvals, done', () => check('/approvals?tab=done', () => screen.getByRole('heading', { name: 'Today' })));
  it('settings: general', () => check('/settings/general', () => screen.getByText('Remote access')));
  it('settings: models', () => check('/settings/models', () => screen.getByText('Model chains')));
  it('settings: policy', () => check('/settings/policy', () => screen.getByText('Standing permissions')));
  // v0.2 folds budgets into General (docs/design/ui.md §4.5), so the old address lands there.
  it('settings: budgets, now part of General', () => check('/settings/budgets', () => screen.getByText('Remote access')));
  it('settings: chat apps', () => check('/settings/channels', () => screen.getByText('Link your accounts')));
  it('settings: vault', () => check('/settings/vault', () => screen.getByText('anthropic_api_key')));
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

  it('setup wizard', () => check('/setup', () => screen.getByText('Welcome to your own assistant')));
});
