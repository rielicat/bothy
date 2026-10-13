import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

const routes = [
  '/chat',
  '/chat/ses_01jtrip000000000000000000',
  '/approvals',
  '/schedules',
  '/schedules/sch_01jbrief00000000000000000',
  '/memory',
  '/usage',
  '/audit',
  '/runs/run_01jtrip1000000000000000',
  '/settings/general',
  '/settings/models',
  '/settings/channels',
  '/settings/policy',
  '/settings/sandbox',
  '/settings/mcp',
  '/settings/vault',
  '/settings/tokens',
  '/settings/advanced',
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

test.describe('axe', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`including contrast, on chat, approvals and settings (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of ['/chat/ses_01jtrip000000000000000000', '/approvals', '/approvals?status=parked', '/settings/general', '/settings/policy', '/settings/budgets']) {
      await page.goto(r);
      await page.waitForLoadState('networkidle');
        expect(await axe(page), `${r} (${scheme})`).toEqual([]);
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
