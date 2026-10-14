import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { probe } from './lib/shots';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

// Every route of docs/design/ui.md §4.5 inside the app, with fixture ids.
const routes = [
  '/chat',
  '/chat/ses_01jtrip000000000000000000',
  '/approvals',
  '/approvals?tab=done',
  '/approvals/apr_01jshell00000000000000000',
  '/knowledge',
  '/knowledge/search?q=redirect',
  '/knowledge/memory',
  '/knowledge/skills',
  '/knowledge/skills/release-notes',
  '/knowledge/projects/acme/web',
  '/activity',
  '/activity/tasks/run_01jtrip1000000000000000',
  '/activity/schedules',
  '/activity/schedules/sch_01jbrief00000000000000000',
  '/activity/schedules/new',
  '/activity/schedules/checkin',
  '/activity/releases',
  '/activity/spending',
  '/activity/audit',
  '/settings/account',
  '/settings/general',
  '/settings/general/personality',
  '/settings/models',
  '/settings/connections',
  '/settings/connections/slack',
  '/settings/connections/telegram',
  '/settings/connections/discord',
  '/settings/connections/github',
  '/settings/connections/search',
  '/settings/connections/mcp-docs',
  '/settings/people',
  '/settings/security',
  '/settings/security/permissions',
  '/settings/security/websites',
  '/settings/security/keys',
  '/settings/advanced',
];

// Outside the app frame: setup, sign in and the first password (§5).
const publicRoutes = ['/setup?mock=setup', '/login?mock=logged-out', '/password'];

// §4.5: links already sent to chat apps and bookmarks keep working.
const redirects: [string, string][] = [
  ['/runs/run_01jtrip1000000000000000?x=1', '/activity/tasks/run_01jtrip1000000000000000?x=1'],
  ['/schedules', '/activity/schedules'],
  ['/schedules/sch_01jbrief00000000000000000', '/activity/schedules/sch_01jbrief00000000000000000'],
  ['/memory', '/knowledge/memory'],
  ['/usage', '/activity/spending'],
  ['/audit?tab=egress', '/activity/audit?tab=egress'],
  ['/settings/channels', '/settings/connections'],
  ['/settings/mcp', '/settings/connections'],
  ['/settings/policy', '/settings/security'],
  ['/settings/sandbox', '/settings/security'],
  ['/settings/vault', '/settings/security/keys'],
  ['/settings/tokens', '/settings/account'],
  ['/settings/persona', '/settings/general'],
  ['/settings/budgets', '/settings/general'],
  ['/settings/skills', '/knowledge/skills'],
  ['/settings', '/settings/general'],
];

// Collects CSP violations and page errors; the build must run under the
// gateway's policy (no inline scripts or styles, no other origins).
function watch(page: Page) {
  const problems: string[] = [];
  page.on('console', (m) => m.type() === 'error' && /Content Security Policy|Refused to/.test(m.text()) && problems.push(m.text()));
  page.on('pageerror', (e) => problems.push(String(e)));
  page.on('request', (r) => !r.url().startsWith('http://localhost:') && !r.url().startsWith('data:') && problems.push(`external request ${r.url()}`));
  return problems;
}

async function axe(page: Page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const r = await (window as unknown as { axe: { run: (n: Node) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run(document);
    return r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  });
}

test('every screen fits a 360 px phone without sideways scrolling', async ({ page }) => {
  test.setTimeout(240_000);
  const problems = watch(page);
  await page.setViewportSize({ width: 360, height: 760 });
  await page.goto('/chat?mock=');
  // The public routes switch the mock to signed out, so they come last.
  for (const r of [...routes, '/settings', ...publicRoutes]) {
    await page.goto(r);
    await page.waitForLoadState('networkidle');
    expect(await page.evaluate(() => document.documentElement.scrollWidth), r).toBeLessThanOrEqual(360);
  }
  expect(problems).toEqual([]);
});

test('every v0.2 address shows a page and every v0.1 address moves to its new one', async ({ page }) => {
  test.setTimeout(240_000);
  const problems = watch(page);
  await page.goto('/chat?mock=');
  for (const r of routes) {
    await page.goto(r);
    await expect(page.locator('h1').first(), r).toBeVisible();
    expect(new URL(page.url()).pathname, r).toBe(new URL(r, 'http://x').pathname);
    await expect(page.getByText("This page doesn't exist."), r).toHaveCount(0);
  }
  for (const [from, to] of redirects) {
    await page.goto(from);
    await expect.poll(() => new URL(page.url()).pathname + new URL(page.url()).search, from).toBe(to);
  }
  // replaceState, not pushState: Back leaves the moved address behind.
  await page.goto('/chat');
  await page.goto('/memory');
  await expect(page).toHaveURL(/\/knowledge\/memory$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/chat$/);
  expect(problems).toEqual([]);
});

// §3.3: one left edge, x = 408 at 1280 px, on every page and the chat.
test('every page and the chat start at the same left edge', async ({ page }) => {
  test.setTimeout(240_000);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/chat?mock=');
  for (const r of routes) {
    await page.goto(r);
    await page.waitForLoadState('networkidle');
    expect(await probe(page, { leftEdge: true, overflow: false, minText: false, blur: false, shadows: false, nesting: false, targets: false }), r).toEqual([]);
  }
});

test.describe('axe', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`including contrast, on every route (${scheme})`, async ({ page }) => {
      test.setTimeout(480_000);
      await page.emulateMedia({ colorScheme: scheme });
      await page.goto('/chat?mock=');
      for (const r of [...routes, '/approvals?status=parked', ...publicRoutes]) {
        await page.goto(r);
        await page.waitForLoadState('networkidle');
        expect.soft(await axe(page), `${r} (${scheme})`).toEqual([]);
      }
    });
  }
});

// The shortcut path (J/K/Enter/A/S/D) is in approvals.spec.ts.
test('approve from the keyboard alone', async ({ page }) => {
  const problems = watch(page);
  await page.goto('/approvals?mock=');
  await expect(page.locator('[data-row] article')).toHaveCount(1);
  // Tab through the page until the first decision button has focus, then press Enter.
  for (let i = 0; i < 80; i++) {
    await page.keyboard.press('Tab');
    if ((await page.evaluate(() => document.activeElement?.getAttribute('data-choice'))) === 'allow_once') break;
  }
  await expect(page.locator(':focus')).toHaveText('Approve deploy');
  await page.keyboard.press('Enter');
  await expect(page.locator('[aria-live="polite"]').first()).toHaveText(/^Approved\. Next: /);
  await page.goto('/approvals?tab=done');
  await expect(page.getByText(/approved deploying acme\/web to production/).first()).toBeVisible();
  expect(problems).toEqual([]);
});

test('chat streams a reply and an inline approval under the CSP', async ({ page }) => {
  const problems = watch(page);
  await page.goto('/chat/ses_01jbudget0000000000000000');
  await page.getByRole('textbox', { name: 'Message' }).fill('Please run a script to tidy my reports');
  await page.keyboard.press('Enter');
  const card = page.getByRole('article').filter({ hasText: 'tidy.py' });
  await expect(card).toBeVisible({ timeout: 10_000 });
  await card.getByRole('button', { name: 'Run command' }).click();
  await expect(page.getByText('The dry run found')).toBeVisible({ timeout: 10_000 });
  expect(problems).toEqual([]);
});
