// C1 shell and navigation (docs/design/ui.md §18.3) against the production
// build under the gateway's CSP: shortcuts, redirects, the chat list rules,
// who sees Settings, plain copy, axe in both themes and the keyboard paths.
import { expect, test, type Browser, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

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

const sidebar = (page: Page) => page.getByRole('navigation', { name: 'Main' });

async function phone(browser: Browser, baseURL: string | undefined, colorScheme: 'light' | 'dark' = 'light', bypassCSP = false) {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 360, height: 780 }, hasTouch: true, isMobile: true, colorScheme, bypassCSP });
  return ctx.newPage();
}

test('⌘K, "/", "?" and Esc open and close search and the shortcuts', async ({ page }) => {
  const problems = watch(page);
  await page.goto('/activity?mock=team-chats');
  await sidebar(page).getByRole('link', { name: 'Fix login redirect' }).waitFor();

  await page.keyboard.press('ControlOrMeta+k');
  const search = page.getByRole('dialog', { name: 'Search' });
  await expect(search).toBeVisible();
  await expect(page.getByRole('combobox', { name: 'Search chats' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(search).toBeHidden();

  await page.keyboard.press('/');
  await expect(search).toBeVisible();
  await page.keyboard.type('launch');
  await expect(page.getByRole('listbox', { name: 'Chats' }).getByRole('option').first()).toContainText('v2.3 launch checklist');
  await page.keyboard.press('Escape');
  await expect(search).toBeHidden();

  await page.keyboard.press('Shift+Slash');
  const keys = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
  await expect(keys).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(keys).toBeHidden();

  await page.keyboard.press('ControlOrMeta+Shift+o');
  await expect(page).toHaveURL(/\/chat$/);
  expect(problems).toEqual([]);
});

test('search opens the chat it found, and Esc gives focus back', async ({ page }) => {
  await page.goto('/activity?mock=team-chats');
  const trigger = sidebar(page).getByRole('button', { name: /^Search/ });
  await trigger.click();
  await page.keyboard.type('pricing');
  await expect(page.getByRole('dialog').getByRole('option').first()).toContainText('Q3 pricing notes');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/chat\/ses_01jteampricing00000000000000#m1$/);

  await page.goto('/activity?mock=team-chats');
  await sidebar(page).getByRole('button', { name: /^Search/ }).click();
  await page.keyboard.press('Escape');
  await expect(sidebar(page).getByRole('button', { name: /^Search/ })).toBeFocused();
});

// §4.5: links already sent to chat apps and bookmarks keep working.
const redirects: [string, string][] = [
  ['/runs/run_01jtrip1000000000000000', '/activity/tasks/run_01jtrip1000000000000000'],
  ['/schedules', '/activity/schedules'],
  ['/schedules/sch_01jbrief00000000000000000', '/activity/schedules/sch_01jbrief00000000000000000'],
  ['/memory', '/knowledge/memory'],
  ['/usage', '/activity/spending'],
  ['/audit', '/activity/audit'],
  ['/settings/channels', '/settings/connections'],
  ['/settings/mcp', '/settings/connections'],
  ['/settings/policy', '/settings/security'],
  ['/settings/sandbox', '/settings/security'],
  ['/settings/vault', '/settings/security/keys'],
  ['/settings/tokens', '/settings/account'],
  ['/settings/persona', '/settings/general'],
  ['/settings/budgets', '/settings/general'],
  ['/settings/skills', '/knowledge/skills'],
];

test('every row of the redirect table lands on its new route', async ({ page }) => {
  const problems = watch(page);
  for (const [from, to] of redirects) {
    await page.goto(`${from}?x=1#h`);
    await expect.poll(() => new URL(page.url()).pathname, from).toBe(to);
    expect(new URL(page.url()).search + new URL(page.url()).hash, from).toBe('?x=1#h');
  }
  expect(problems).toEqual([]);
});

test('a chat without messages never appears in the list', async ({ page }) => {
  await page.goto('/activity?mock=');
  const rows = sidebar(page).locator('a[href^="/chat/"]');
  await rows.first().waitFor();
  const before = await rows.allTextContents();
  const made = await page.request.post('/api/sessions', { data: {}, headers: { 'X-CSRF-Token': 'mock-csrf' } });
  expect(made.ok()).toBe(true);
  const { id } = (await made.json()) as { id: string };
  await page.reload();
  await rows.first().waitFor();
  await page.waitForLoadState('networkidle');
  expect(await rows.allTextContents()).toEqual(before);
  await expect(sidebar(page).locator(`a[href="/chat/${id}"]`)).toHaveCount(0);
  expect((await rows.allTextContents()).map((t) => t.trim())).not.toContain('');
});

test('members see no Settings item, and an admin page tells them it is for admins', async ({ page }) => {
  await page.goto('/activity?mock=member');
  await sidebar(page).getByRole('button', { name: /^Account:/ }).waitFor();
  await expect(sidebar(page).getByRole('link', { name: /Settings/ })).toHaveCount(0);
  await sidebar(page).getByRole('button', { name: /^Account:/ }).click();
  await expect(page.getByRole('menuitem', { name: /Pause/ })).toHaveCount(0);
  await page.getByRole('menuitem', { name: 'Your account' }).click();
  await expect(page).toHaveURL(/\/settings\/account$/);
  await expect(sidebar(page).getByRole('link')).toHaveText(['Back to Bothy', 'Account']);
  await page.goto('/settings/general');
  await expect(page.getByText('Only admins can see this page.')).toBeVisible();
});

test('admins see Settings with its dot, and settings mode swaps the sidebar', async ({ page }) => {
  await page.goto('/activity?mock=team-chats');
  await expect(sidebar(page).getByRole('link', { name: /^Approvals/ })).toHaveAccessibleName('Approvals 2 waiting');
  const settings = sidebar(page).getByRole('link', { name: /^Settings/ });
  await expect(settings).toHaveAccessibleName('Settings Needs attention');
  await settings.click();
  await expect(page).toHaveURL(/\/settings\/general$/);
  const back = sidebar(page).getByRole('link', { name: 'Back to Bothy' });
  await expect(back).toHaveAttribute('href', /^\/activity(\?|$)/);
  await back.click();
  await expect(page).toHaveURL(/\/activity(\?|$)/);
});

test('Tab starts at Skip to content, which moves to the page', async ({ page }) => {
  await page.goto('/activity?mock=team-chats');
  await page.getByRole('heading', { level: 1, name: 'Activity' }).waitFor();
  await page.keyboard.press('Tab');
  const skip = page.getByRole('link', { name: 'Skip to content' });
  await expect(skip).toBeFocused();
  await expect(skip).toBeVisible();
  await page.keyboard.press('Enter');
  await expect(page.locator('main')).toBeFocused();
});

test('after a route change focus is on the new page’s h1', async ({ page }) => {
  await page.goto('/activity?mock=team-chats');
  await sidebar(page).getByRole('link', { name: 'Knowledge' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Knowledge' })).toBeFocused();
});

// Route groups are lazy chunks (docs/design/ui.md §18.2 G1): "activity" for /assets/activity-<hash>.js.
function chunks(page: Page) {
  const seen = new Set<string>();
  page.on('request', (r) => {
    const m = /^\/assets\/([A-Za-z]+)-[\w-]+\.js$/.exec(new URL(r.url()).pathname);
    if (m) seen.add(m[1]);
  });
  return seen;
}

test('the first screen loads one script, and a hovered link fetches its chunk before the click', async ({ page }) => {
  const problems = watch(page);
  // Save-Data turns idle prefetching off, so only the hover can fetch a chunk.
  await page.addInitScript(() => Object.defineProperty(navigator, 'connection', { value: { saveData: true } }));
  const seen = chunks(page);
  await page.goto('/chat');
  await expect(page.getByRole('heading', { level: 1, name: 'What should Bothy work on?' })).toBeVisible();
  expect([...seen]).toEqual(['index']);
  const activity = sidebar(page).getByRole('link', { name: 'Activity' });
  await activity.hover();
  await expect.poll(() => seen.has('activity')).toBe(true);
  await activity.click();
  await expect(page.getByRole('heading', { level: 1, name: 'Activity' })).toBeFocused();
  expect(problems).toEqual([]);
});

test('once someone is signed in, idle time loads every route chunk', async ({ page }) => {
  const problems = watch(page);
  const seen = chunks(page);
  await page.goto('/chat');
  await expect.poll(() => ['approvals', 'activity', 'knowledge', 'settings'].filter((c) => !seen.has(c)), { timeout: 20_000 }).toEqual([]);
  expect(seen.has('auth')).toBe(false);
  expect(problems).toEqual([]);
});

test('the account menu works from the keyboard, Theme included', async ({ page }) => {
  await page.goto('/activity?mock=team-chats');
  const trigger = sidebar(page).getByRole('button', { name: 'Account: Maya Chen' });
  await trigger.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('menuitem', { name: 'Your account' })).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('menuitemradio', { name: 'System' })).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(trigger).toBeFocused();
  await page.evaluate(() => localStorage.removeItem('bothy.theme'));
});

test('on a phone the drawer opens from the top bar, closes with Esc and gives focus back', async ({ browser, baseURL }) => {
  const page = await phone(browser, baseURL);
  const problems = watch(page);
  await page.goto('/activity?mock=team-chats');
  const menu = page.getByRole('button', { name: 'Open menu, 2 approvals waiting' });
  await expect(page.getByRole('link', { name: '2 waiting' })).toBeVisible();
  await menu.click();
  const drawer = page.getByRole('dialog', { name: 'Menu' });
  await expect(drawer).toBeVisible();
  expect((await drawer.boundingBox())?.width).toBeCloseTo(306, 0);
  await page.keyboard.press('Escape');
  await expect(drawer).toBeHidden();
  await expect(menu).toBeFocused();
  await menu.click();
  await drawer.getByRole('link', { name: 'Knowledge' }).click();
  await expect(drawer).toBeHidden();
  await expect(page).toHaveURL(/\/knowledge$/);
  expect(await page.evaluate(() => document.scrollingElement!.scrollWidth)).toBeLessThanOrEqual(360);
  expect(problems).toEqual([]);
  await page.context().close();
});

test('a banner stays in view in a chat that opens at its latest reply, above the chat title', async ({ browser, baseURL, page }) => {
  const paused = (p: Page) => p.getByRole('status').filter({ hasText: 'paused Bothy' });
  // A new chat, and a chat long enough to open scrolled to the bottom.
  for (const path of ['/chat?mock=paused', '/chat/ses_0chatlogin0000000000000000?mock=paused']) {
    await page.goto(path);
    await expect(paused(page)).toBeInViewport({ ratio: 1 });
  }
  await expect(page.getByRole('button', { name: 'Fix login redirect', exact: true })).toBeInViewport({ ratio: 1 });
  const banner = await paused(page).boundingBox();
  const title = await page.getByRole('button', { name: 'Fix login redirect', exact: true }).boundingBox();
  expect(title!.y).toBeGreaterThanOrEqual(banner!.y + banner!.height);

  const small = await phone(browser, baseURL);
  await small.goto('/chat/ses_0chatstaging00000000000000?mock=paused');
  await expect(paused(small)).toBeInViewport({ ratio: 1 });
  await small.context().close();
});

test('a request opened from its link is scrolled clear of the banner and the phone top bar', async ({ browser, baseURL, page }) => {
  const small = await phone(browser, baseURL);
  for (const p of [page, small]) {
    await p.goto('/approvals/apr_01jpr00000000000000000000?mock=paused');
    const card = p.locator('article[data-approval="apr_01jpr00000000000000000000"]');
    await expect(card).toBeInViewport();
    await expect.poll(() => p.evaluate(() => window.scrollY)).toBeGreaterThan(0);
    const banner = (await p.getByRole('status').filter({ hasText: 'paused Bothy' }).boundingBox())!;
    expect((await card.boundingBox())!.y).toBeGreaterThanOrEqual(banner.y + banner.height);
  }
  await small.context().close();
});

test('on a phone the top bar names the page once a banner hides its heading', async ({ browser, baseURL }) => {
  const page = await phone(browser, baseURL);
  await page.goto('/approvals?mock=paused');
  await expect(page.locator('article').first()).toBeVisible();
  const banner = (await page.getByRole('status').filter({ hasText: 'paused Bothy' }).boundingBox())!;
  const h1 = (await page.getByRole('heading', { level: 1, name: 'Approvals' }).boundingBox())!;
  // Behind the banner, but not yet behind the bar above it.
  await page.evaluate((y) => window.scrollTo(0, y), h1.y + h1.height - (banner.y + banner.height) + 4);
  await expect(page.getByRole('button', { name: /^Open menu/ }).locator('..').getByText('Approvals', { exact: true })).toBeVisible();
  await page.context().close();
});

test('controls are 44 px under a coarse pointer', async ({ browser, baseURL }) => {
  const page = await phone(browser, baseURL);
  await page.goto('/activity?mock=team-chats');
  await page.getByRole('button', { name: /^Open menu/ }).click();
  const drawer = page.getByRole('dialog', { name: 'Menu' });
  await drawer.getByRole('link', { name: 'Fix login redirect' }).waitFor();
  for (const el of await drawer.locator('a[href], button').all()) {
    const box = await el.boundingBox();
    if (!box) continue;
    expect(Math.round(box.height), (await el.textContent()) ?? '').toBeGreaterThanOrEqual(44);
  }
  await page.context().close();
});

const copyStates: [string, (page: Page) => Promise<unknown>][] = [
  ['/activity?mock=team-chats', async () => {}],
  ['/activity?mock=team-chats', (p) => sidebar(p).getByRole('button', { name: /^Account:/ }).click()],
  ['/activity?mock=team-chats', async (p) => (await sidebar(p).getByRole('button', { name: /^Account:/ }).click(), p.getByRole('menuitem', { name: 'Theme' }).click())],
  ['/activity?mock=team-chats', (p) => p.keyboard.press('Shift+Slash')],
  ['/activity?mock=team-chats', async (p) => (await p.keyboard.press('/'), p.keyboard.type('launch'), p.getByRole('dialog').getByRole('option').first().waitFor())],
  ['/activity?mock=team-chats', async (p) => (await sidebar(p).getByRole('button', { name: /^Account:/ }).click(), p.getByRole('menuitem', { name: 'Pause Bothy…' }).click())],
  ['/activity?mock=team-chats,paused', async () => {}],
  ['/activity?mock=team-chats,provider-auth', async () => {}],
  ['/activity?mock=team-chats,safe-mode', async () => {}],
  ['/activity?mock=team-chats,spend-limit', async () => {}],
  ['/activity?mock=team-chats,restart', async () => {}],
  ['/activity?mock=team-chats,sealed', async () => {}],
  ['/settings/connections/github?mock=team-chats,sealed', async () => {}],
  ['/settings/connections/github?mock=team-chats', async () => {}],
  ['/nope?mock=team-chats', async () => {}],
  ['/activity/audit?mock=team-chats,member', async () => {}],
];

test('shell copy follows the copy guide', async ({ page }) => {
  for (const [route, setup] of copyStates) {
    await page.goto(route);
    await page.waitForLoadState('networkidle');
    await setup(page);
    await page.waitForTimeout(150);
    await assertPlainCopy(page);
  }
});

test.describe('axe on the shell', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`wide screen (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const [route, setup] of copyStates) {
        await page.goto(route);
        await page.waitForLoadState('networkidle');
        await setup(page);
        await page.waitForTimeout(250);
        expect.soft(await axe(page), `${route} (${scheme})`).toEqual([]);
      }
    });

    test(`phone (${scheme})`, async ({ browser, baseURL }) => {
      const page = await phone(browser, baseURL, scheme, true);
      const open = (p: Page) => p.getByRole('button', { name: /^Open menu/ }).click();
      const cases: [string, (p: Page) => Promise<unknown>][] = [
        ['/activity?mock=team-chats', async () => {}],
        ['/activity?mock=team-chats', open],
        ['/activity?mock=team-chats', async (p) => (await open(p), p.getByRole('dialog', { name: 'Menu' }).getByRole('button', { name: /^Account:/ }).click())],
        ['/settings?mock=team-chats', async () => {}],
        ['/activity?mock=team-chats,restart', async () => {}],
      ];
      for (const [route, setup] of cases) {
        await page.goto(route);
        await page.waitForLoadState('networkidle');
        await setup(page);
        await page.waitForTimeout(300);
        expect.soft(await axe(page), `${route} (${scheme}, phone)`).toEqual([]);
      }
      await page.context().close();
    });
  }
});
