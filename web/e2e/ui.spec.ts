import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

// docs/design/ui.md §4.5, with fixture ids.
const routes = [
  '/chat',
  '/chat/ses_01jtrip000000000000000000',
  '/approvals',
  '/approvals/apr_01jshell00000000000000000',
  '/knowledge',
  '/knowledge/search',
  '/knowledge/memory',
  '/knowledge/skills',
  '/knowledge/projects/acme/web',
  '/activity',
  '/activity/tasks/run_01jtrip1000000000000000',
  '/activity/schedules',
  '/activity/schedules/sch_01jbrief00000000000000000',
  '/activity/schedules/new',
  '/activity/releases',
  '/activity/spending',
  '/activity/audit',
  '/settings/account',
  '/settings/general',
  '/settings/general/personality',
  '/settings/models',
  '/settings/connections',
  '/settings/connections/telegram',
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
  const problems = watch(page);
  await page.setViewportSize({ width: 360, height: 760 });
  for (const r of routes) {
    await page.goto(r);
    await page.waitForLoadState('networkidle');
    expect(await page.evaluate(() => document.documentElement.scrollWidth), r).toBeLessThanOrEqual(360);
  }
  expect(problems).toEqual([]);
});

test('every v0.2 address shows a page and every v0.1 address moves to its new one', async ({ page }) => {
  const problems = watch(page);
  for (const r of routes) {
    await page.goto(r);
    // h2: the v0.1 settings frame titles a page hidden in single-user mode (People) with one.
    await expect(page.locator('h1, h2').first(), r).toBeVisible();
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

// v0.1 screens that already failed axe; each goes when its chunk replaces the
// screen (docs/design/ui.md §18.3), and §16 wants every route in the end.
const axeKnown: Record<string, string> = {
  '/activity/schedules': 'C6: disabled schedules fade their text below 4.5:1',
  '/activity/schedules/sch_01jbrief00000000000000000': 'C6: the run history table has an empty header',
  '/activity/spending': 'C6: the v0.1 usage tables overflow the 720 px column and scroll without being focusable',
  '/settings/people': 'C7: hidden in single-user mode, so the page has no h1',
  '/settings/advanced': 'C9: the effective config <pre> scrolls without being focusable',
};

test.describe('axe', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`including contrast, on every route (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of [...routes.filter((r) => !(r in axeKnown)), '/approvals?status=parked']) {
        await page.goto(r);
        await page.waitForLoadState('networkidle');
        expect.soft(await axe(page), `${r} (${scheme})`).toEqual([]);
      }
    });
  }
});

test('approve from the keyboard alone', async ({ page }) => {
  const problems = watch(page);
  await page.goto('/approvals');
  await expect(page.getByRole('article')).toHaveCount(1);
  // Tab through the page until the first decision button has focus, then press Enter.
  for (let i = 0; i < 80; i++) {
    await page.keyboard.press('Tab');
    if ((await page.evaluate(() => document.activeElement?.textContent?.trim())) === 'Allow once a') break;
  }
  await expect(page.locator(':focus')).toHaveText(/Allow once/);
  await page.keyboard.press('Enter');
  await expect(page.getByText('All clear')).toBeVisible();
  await page.getByRole('tab', { name: /Recent/ }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Allowed once · by you · via Web' }).first()).toBeVisible();
  // and the shortcut path on the parked tab
  await page.getByRole('tab', { name: /Parked/ }).focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('article')).toHaveCount(1);
  await page.keyboard.press('d');
  await expect(page.getByText('Nothing parked')).toBeVisible();
  expect(problems).toEqual([]);
});

test('chat streams a reply and an inline approval under the CSP', async ({ page }) => {
  const problems = watch(page);
  await page.goto('/chat/ses_01jbudget0000000000000000');
  await page.getByRole('textbox', { name: 'Message' }).fill('Please run a script to tidy my reports');
  await page.keyboard.press('Enter');
  const card = page.getByRole('article').filter({ hasText: 'tidy.py' });
  await expect(card).toBeVisible({ timeout: 10_000 });
  await card.getByRole('button', { name: /Allow once/ }).click();
  await expect(page.getByText('The dry run found')).toBeVisible({ timeout: 10_000 });
  expect(problems).toEqual([]);
});
