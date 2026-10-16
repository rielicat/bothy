// C4 Approvals (docs/design/ui.md §8, §18.3): the keyboard path through the
// inbox, who sees which buttons, and where red is allowed. Runs in the smoke
// run against the production build and the fixture gateway.
import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';
import { probe } from './lib/shots';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

const SHELL = 'apr_01jshell00000000000000000';
const DELETE = 'apr_01jdelete0000000000000000';
const MESSAGE = 'apr_01jfetch00000000000000000';

const SHELL_HIDDEN = 'nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"], a[href="#main"] { display: none !important; }';

const live = (page: Page) => page.locator('[aria-live="polite"]').first();
const openCard = (page: Page) => page.locator('[data-row] article').first();

async function inbox(page: Page, mock = '') {
  await page.goto(`/approvals?mock=${mock}`);
  await page.locator('[data-row]').first().waitFor();
}

test.describe('approvals inbox', () => {
  test('J, K, Enter, A, S and D work without a pointer, one decision per key press', async ({ page }) => {
    await inbox(page);
    await expect(openCard(page)).toHaveAttribute('data-approval', 'apr_01jdeployweb0000000000000');

    // J moves to the next row, Enter opens it and takes focus into the card.
    await page.keyboard.press('j');
    await expect(page.locator(`[data-row="a:${SHELL}"] button[aria-expanded="false"]`)).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.locator(`article[data-approval="${SHELL}"]`)).toBeFocused();

    // S allows for this chat; focus and the announcement move to the next request.
    await page.keyboard.press('s');
    await expect(live(page)).toHaveText(/Allowed for this chat\. Next: Send a message to the Telegram chat\?/);
    await expect(page.locator(`article[data-approval="${MESSAGE}"]`)).toBeFocused();

    // A held key decides one request: the auto-repeat that follows is ignored until the key is released.
    await page.keyboard.down('a');
    await expect(live(page)).toHaveText(/Approved\. Next: Delete notes\/staging\.md\?/);
    await page.keyboard.down('a');
    await page.keyboard.down('a');
    await page.waitForTimeout(300);
    await expect(page.locator(`article[data-approval="${DELETE}"]`)).toBeVisible();
    await page.keyboard.up('a');

    // D asks for an optional reason; Enter sends the deny.
    await page.keyboard.press('d');
    const reason = page.getByLabel('Reason (optional)');
    await expect(reason).toBeFocused();
    await reason.fill('Keep it for the audit');
    await page.keyboard.press('Enter');
    await expect(live(page)).toHaveText(/Denied\. Next: /);

    // K moves back up through the rows.
    await page.keyboard.press('k');
    await expect(page.locator('[data-row] button[aria-expanded="false"]:focus, [data-row] article:focus')).toHaveCount(1);

    await page.goto('/approvals?tab=done');
    await expect(page.getByText('You denied deleting notes/staging.md')).toBeVisible();
    await expect(page.getByText('“Keep it for the audit”')).toBeVisible();
  });

  test('Let in, and Turn on with what Bothy may do there, without a pointer', async ({ page }) => {
    await inbox(page);
    await page.locator('[data-row="i:sender:telegram:7215"] button[aria-expanded="false"]').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('article[data-item="sender:telegram:7215"]')).toBeFocused();
    await page.keyboard.press('a');
    await expect(live(page)).toHaveText(/Let in\. Next: /);

    // The next item may already have opened in its place, with focus.
    const card = page.locator('article[data-item="group:spc_01jsupport0000000000000000"]');
    if (!(await card.isVisible())) {
      await page.locator('[data-row="i:group:spc_01jsupport0000000000000000"] button[aria-expanded="false"]').focus();
      await page.keyboard.press('Enter');
    }
    await expect(card).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(card.getByRole('radio', { name: 'Answer questions' })).toBeFocused();
    // Inside the choice, keys move between the options and decide nothing.
    await page.keyboard.press('ArrowDown');
    await expect(card.getByRole('radio', { name: 'Do work, with approval' })).toBeChecked();
    await page.keyboard.press('Tab');
    await expect(card.getByRole('button', { name: 'Turn on' })).toBeFocused();
    const patched = page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/spaces/spc_01jsupport0000000000000000'));
    await page.keyboard.press('a');
    expect(JSON.parse((await patched).postData() ?? '{}')).toEqual({ enabled: true, tool_profile: 'standard' });
    await expect(live(page)).toHaveText(/Turned on\. Next: /);
  });

  test('the person who asked for a deploy sees no decision buttons', async ({ page }) => {
    await inbox(page, 'member');
    const deploy = page.locator('[data-row="a:apr_01jdeployweb0000000000000"]');
    await expect(page.getByRole('heading', { name: 'Waiting for others' })).toBeVisible();
    await deploy.locator('button[aria-expanded]').click();
    const card = deploy.locator('article');
    // "…so 2 other people need to approve it." until the votes load, then the count so far.
    await expect(card.getByText(/^You asked for this/)).toBeVisible();
    await expect(card.locator('button[data-choice]')).toHaveCount(0);
    await expect(card.getByRole('button', { name: 'Cancel request' })).toBeVisible();
  });

  test('deploys never offer "Allow for this chat"; red marks only what cannot be undone or spends money', async ({ page, request }) => {
    await inbox(page, 'approvals-all');
    const list = (await (await request.get('/api/approvals?status=pending', { headers: { cookie: 'bothy_mock=approvals-all' } })).json()).items as {
      id: string;
      action: { tool: string; side_effect: string };
    }[];
    const parked = (await (await request.get('/api/approvals?status=parked', { headers: { cookie: 'bothy_mock=approvals-all' } })).json()).items;
    const all = [...list, ...parked];
    expect(all.length).toBeGreaterThan(15);
    for (const a of all) {
      await page.goto(`/approvals/${a.id}`);
      const card = page.locator(`article[data-approval="${a.id}"]`);
      await card.waitFor();
      const buttons = card.locator('button[data-choice]');
      if (!(await buttons.count())) continue;
      const deploy = a.action.tool === 'release_deploy';
      if (deploy) await expect(card.getByRole('button', { name: /Allow for this chat|Always allow/ }), a.id).toHaveCount(0);
      const deny = card.locator('button[data-choice="deny"]');
      if (await deny.count()) await expect(deny, a.id).not.toHaveClass(/danger/);
      const red = ['irreversible', 'financial'].includes(a.action.side_effect) || a.action.tool === 'schedule_delete';
      const primary = card.locator('button[data-choice="allow_once"]');
      if (await primary.count()) {
        if (red && !deploy) await expect(primary, a.id).toHaveClass(/btn-danger/);
        else await expect(primary, a.id).not.toHaveClass(/danger/);
      }
      await expect(card.locator('.btn-danger')).toHaveCount(red && !deploy && (await primary.count()) ? 1 : 0);
    }
  });

  test('a card opened from a chat-app link lands expanded and scrolled into view', async ({ page }) => {
    await page.goto(`/approvals/apr_01jpr00000000000000000000?mock=`);
    const card = page.locator('article[data-approval="apr_01jpr00000000000000000000"]');
    await expect(card).toBeInViewport();
    await expect(card.getByRole('group', { name: 'Changes to src/auth/redirect.ts' })).toBeVisible();
  });

  test('content starts on the shared column edge', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await inbox(page);
    // The column is 768 px centred in the main area, with 24 px padding (§3.3); the shell sets where the main area starts.
    const { h1, main } = await page.evaluate(() => {
      const m = document.querySelector('main') ?? document.getElementById('main')!;
      const r = m.getBoundingClientRect();
      return { h1: document.querySelector('h1')!.getBoundingClientRect().left, main: { left: r.left, width: m.clientWidth } };
    });
    expect(Math.abs(h1 - (main.left + (main.width - 768) / 2 + 24))).toBeLessThanOrEqual(1);
  });
});

test.describe('approvals, copy and accessibility', () => {
  test.use({ bypassCSP: true });
  const routes = ['/approvals', '/approvals?tab=done', '/approvals/apr_01jpr00000000000000000000', '/approvals?mock=member'];

  for (const scheme of ['light', 'dark'] as const) {
    test(`axe passes with contrast (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of routes) {
        await page.goto(r.includes('mock=') ? r : `${r}${r.includes('?') ? '&' : '?'}mock=`);
        await page.waitForLoadState('networkidle');
        await page.addScriptTag({ path: axePath });
        const v = await page.evaluate(async () => {
          const r = await (window as unknown as { axe: { run: (n: Node, o: unknown) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run(
            document.querySelector('#main') ?? document,
            {},
          );
          return r.violations.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`);
        });
        expect.soft(v, `${r} (${scheme})`).toEqual([]);
      }
    });
  }

  test('the main flow uses plain words', async ({ page }) => {
    for (const r of [...routes, '/approvals?mock=approvals-all']) {
      await page.goto(r.includes('mock=') ? r : `${r}${r.includes('?') ? '&' : '?'}mock=`);
      await page.waitForLoadState('networkidle');
      // The v0.1 shell around the page is not part of this chunk.
      await assertPlainCopy(page, { ignore: ['nav[aria-label="Main"]', 'header', '[role="region"][aria-label="Notices"]'] });
    }
  });

  test('controls are 44 px on touch and nothing scrolls sideways', async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 360, height: 780 }, hasTouch: true, isMobile: true });
    const page = await ctx.newPage();
    for (const r of ['/approvals?mock=', '/approvals/apr_01jpr00000000000000000000?mock=', '/approvals?tab=done&mock=']) {
      await page.goto(r);
      await page.locator('h1').waitFor();
      await page.waitForLoadState('networkidle');
      // Only this chunk's page: the v0.1 shell around it is replaced by C1.
      await page.evaluate((css) => {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(css);
        document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
      }, SHELL_HIDDEN);
      expect(await probe(page, { targets: true, overflow: true, leftEdge: false, minText: true, blur: false, shadows: false, nesting: false }), r).toEqual([]);
    }
    await ctx.close();
  });
});
