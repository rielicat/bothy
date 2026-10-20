// C4 Approvals (docs/design/ui.md §8, §18.3): a card for every action of
// §8.2, warnings, deny reason, sending and failed states, the "Always allow"
// dialog, every decided line, the multi-approver viewer states, the pull
// request card, the deploy follow-up lines, the inbox, the Done tab, each
// review item and the empty states, at 1280 and 375 in light and dark.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/approvals.spec.ts
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

// Until C1 lands the inbox renders inside the v0.1 shell (240 px sidebar, top
// bar, banner strip). This puts the page in the v0.2 frame instead, as the
// primitives spec does, so captures and probes judge this chunk's screens.
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

async function frame(page: Page) {
  // A constructed sheet, since the gateway's CSP refuses inline <style>.
  await page.evaluate((css) => {
    // Only around the v0.1 shell; C1's shell is part of what the captures judge.
    if (!document.querySelector('div:has(> #main) > header')) return;
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }, V02_FRAME);
  await page.locator('h1').waitFor();
  await page.locator('[data-row], [data-decided], .text-reading').first().waitFor();
  // /approvals/:id scrolled its card into view inside the old shell's own scroll box; the frame moved it.
  const id = /^\/approvals\/([^/?]+)/.exec(new URL(page.url()).pathname)?.[1];
  const opened = page.locator(`article[data-approval="${id}"]`);
  if (id && (await opened.count())) await opened.evaluate((el) => el.closest('li')?.scrollIntoView({ block: 'start' }));
}

const card = (id: string) => (page: Page) => page.locator(`article[data-approval="${id}"]`);
const inCard = (id: string, name: string | RegExp) => async (page: Page) => {
  await card(id)(page).getByRole('button', { name }).first().click();
};
const openRow = (key: string, then?: (page: Page) => Promise<unknown>) => async (page: Page) => {
  await page.locator(`[data-row="${key}"] button[aria-expanded]`).click();
  await page.locator(`[data-row="${key}"] article`).waitFor();
  // The rows moved under the pointer when the card opened; a stale hover would show in the capture.
  await page.mouse.move(0, 0);
  await then?.(page);
};
const showOn = (text: string) => async (page: Page) => {
  await page.locator('li', { has: page.locator('[data-decided]', { hasText: text }) }).first().getByRole('button', { name: 'Show' }).click();
};

// One waiting card per row of §8.2 (default fixtures plus ?mock=approvals-all, whose ids mock/domains/approvals.ts pads).
const row = (key: string) => `apr_01jrow${key}`.padEnd(30, '0');
const rows: [string, string][] = [
  ['card-fs-create', row('create')],
  ['card-fs-change', row('change')],
  ['card-fs-append', row('append')],
  ['card-fs-delete', 'apr_01jdelete0000000000000000'],
  ['card-shell', 'apr_01jshell00000000000000000'],
  ['card-python', row('python')],
  ['card-web-send', row('post')],
  ['card-message-parked', 'apr_01jfetch00000000000000000'],
  ['card-memory', row('memory')],
  ['card-memory-everyone', row('teammemory')],
  ['card-notes', row('notes')],
  ['card-schedule-create', row('schedcreate')],
  ['card-schedule-delete', row('scheddelete')],
  ['card-net-allow', row('netallow')],
  ['card-pull-request', 'apr_01jpr00000000000000000000'],
  ['card-github-comment', row('comment')],
  ['card-deploy-single', row('deploy1')],
  ['card-mcp', row('mcp')],
  ['card-mcp-irreversible', row('mcpirrev')],
  ['card-admin', row('admin')],
  ['card-financial', row('pay')],
  ['card-unknown', row('unknown')],
];

const DEPLOY = 'apr_01jdeployweb0000000000000';
const DELETE = 'apr_01jdelete0000000000000000';
const SHELL = 'apr_01jshell00000000000000000';

const states: ShotState[] = [
  { name: 'inbox', route: '/approvals', ready: frame },
  { name: 'inbox-member', route: '/approvals', mock: 'member', ready: frame },
  { name: 'inbox-team-access', route: '/approvals', mock: 'team-access', ready: frame },
  { name: 'inbox-actions-only', route: '/approvals', mock: 'approvals-no-items', ready: frame },
  // The Friendly mockup's inbox: the hero chat's pull request (the oldest, so it opens first), a newsletter for you and a deploy waiting for someone else.
  { name: 'inbox-hero', route: '/approvals', mock: 'hero', ready: frame, setup: (p) => card('apr_01jheropr'.padEnd(30, '0'))(p).waitFor() },
  { name: 'empty-waiting', route: '/approvals', mock: 'approvals-empty', ready: frame },
  { name: 'empty-done', route: '/approvals?tab=done', mock: 'approvals-empty', ready: frame },
  { name: 'load-failed', route: '/approvals', mock: 'approvals-down', ready: frame },
  { name: 'load-failed-done', route: '/approvals?tab=done', mock: 'approvals-down', ready: frame },
  { name: 'done', route: '/approvals?tab=done', ready: frame, full: true },
  { name: 'done-show-multi-approved', route: '/approvals?tab=done', ready: frame, setup: showOn('Maya Chen and Priya Rao approved') },
  { name: 'done-show-multi-denied', route: '/approvals?tab=done', ready: frame, setup: showOn('Maya Chen denied') },
  { name: 'already-decided', route: '/approvals/apr_01jdonedel10000000000000', ready: frame },

  ...rows.map(([name, id]): ShotState => ({ name, route: `/approvals/${id}`, mock: 'approvals-all', ready: frame })),

  { name: 'multi-eligible', route: `/approvals/${DEPLOY}`, ready: frame },
  { name: 'multi-voted', route: `/approvals/${DEPLOY}`, mock: 'deploy-voted', ready: frame },
  { name: 'multi-requester', route: `/approvals/${DEPLOY}`, mock: 'deploy-requester', ready: frame },
  { name: 'multi-not-eligible', route: `/approvals/${DEPLOY}`, mock: 'deploy-ineligible', ready: frame },
  { name: 'card-details-open', route: `/approvals/${SHELL}`, ready: frame, setup: inCard(SHELL, 'Details') },
  { name: 'deny-reason', route: `/approvals/${DELETE}`, ready: frame, setup: inCard(DELETE, 'Deny') },
  { name: 'submitting', route: `/approvals/${DELETE}`, mock: 'approvals-hold', ready: frame, setup: inCard(DELETE, 'Delete file') },
  {
    name: 'submit-failed',
    route: `/approvals/${DELETE}`,
    mock: 'approvals-fail',
    ready: frame,
    setup: async (page) => {
      await inCard(DELETE, 'Delete file')(page);
      await page.getByRole('alert').waitFor();
    },
  },
  {
    name: 'always-allow-dialog',
    route: `/approvals/${SHELL}`,
    ready: frame,
    setup: async (page) => {
      await inCard(SHELL, /Always allow/)(page);
      await page.getByRole('dialog').waitFor();
    },
  },

  { name: 'item-mcp-review', route: '/approvals', ready: frame, setup: openRow('i:mcp:docs', (p) => p.getByRole('button', { name: 'Show what changed' }).first().click()) },
  { name: 'item-skill-review', route: '/approvals', ready: frame, setup: openRow('i:skill:incident-review') },
  { name: 'item-skill-review-diff', route: '/approvals', ready: frame, setup: openRow('i:skill:incident-review', (p) => p.getByRole('button', { name: 'Show what changed' }).first().click()) },
  { name: 'item-unknown-sender', route: '/approvals', ready: frame, setup: openRow('i:sender:telegram:7215') },
  { name: 'item-let-in', route: '/approvals', ready: frame, setup: openRow('i:sender:telegram:7215', (p) => p.getByRole('button', { name: 'Let in' }).focus()) },
  { name: 'item-group-chat', route: '/approvals', ready: frame, setup: openRow('i:group:spc_01jsupport0000000000000000') },
  {
    name: 'item-channel-choice',
    route: '/approvals',
    ready: frame,
    setup: openRow('i:group:spc_01jsupport0000000000000000', (p) => p.getByRole('radio', { name: 'Do work, with approval' }).check()),
  },
  { name: 'item-channel-discord', route: '/approvals', ready: frame, setup: openRow('i:group:spc_01jcommunity00000000000000') },
  { name: 'approval-team-card', route: '/approvals/apr_01jteamdm00000000000000000', ready: frame },
  { name: 'approval-team-channel', route: '/approvals/apr_01jteamchan000000000000000', ready: frame },
  // A note suggestion is an approval whose card is a review (S12).
  { name: 'item-note-suggestion', route: '/approvals', mock: 'approvals-all', ready: frame, setup: openRow(`a:${row('notes')}`) },
];

shots('approvals', states);
