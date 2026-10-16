// C8 Settings → Connections (docs/design/ui.md §12, §18.3): axe in both
// themes, plain copy, layout and touch targets, the keyboard paths of the
// option lists and the multi-select, and the connect flows end to end.
import { expect, test, type Browser, type Page, type TestInfo } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';
import { openPage, probe, viewports } from './lib/shots';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

const routes = [
  '/settings/connections',
  '/settings/connections/slack',
  '/settings/connections/telegram',
  '/settings/connections/discord',
  '/settings/connections/github',
  '/settings/connections/search',
  '/settings/connections/mcp-linear',
  '/settings/connections/mcp-docs',
];

// The v0.1 shell around the pages until C1 replaces it; its copy is C1's.
const SHELL = ['nav[aria-label="Main"]', 'div:has(> #main) > header', '[role="region"][aria-label="Notices"]'];

async function axe(page: Page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async (exclude) => {
    const r = await (
      window as unknown as { axe: { run: (n: unknown) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }
    ).axe.run({ exclude: exclude.map((s) => [s]) });
    return r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  }, SHELL);
}

/** Opens a page; with `mock` (even empty) the fixtures start fresh, without it they keep what the test changed. */
async function go(page: Page, route: string, mock?: string) {
  await page.goto(mock === undefined ? route : `${route}${route.includes('?') ? '&' : '?'}mock=${mock}`);
  await page.waitForLoadState('networkidle');
  await page.locator('h1').first().waitFor();
}

// Hides the v0.1 shell so the probes judge these pages (as the shot spec does).
async function frame(page: Page) {
  await page.evaluate((hide) => {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(`${hide.join(',')} { display: none !important; }`);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }, SHELL);
}

test.describe('axe', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`clean on every Connections page and dialog (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of routes) {
        await go(page, r, '');
        expect.soft(await axe(page), `${r} (${scheme})`).toEqual([]);
      }
      for (const mock of ['github-none', 'github-app-pending', 'github-app-retired', 'github-app-made']) {
        await go(page, '/settings/connections/github', mock);
        expect.soft(await axe(page), `github ${mock} (${scheme})`).toEqual([]);
      }
      await go(page, '/settings/connections/github', 'github-none');
      await page.getByRole('button', { name: 'Other ways to connect' }).click();
      await page.getByLabel('Token', { exact: true }).fill('github_pat_11ABCDEFG0123456789abcdef');
      await page.getByLabel('Token', { exact: true }).press('Enter');
      await page.getByText('Bothy can see').waitFor();
      expect.soft(await axe(page), `github connect (${scheme})`).toEqual([]);

      const dialogs: [string, string, string][] = [
        ['/settings/connections', 'Add an MCP server', 'Add an MCP server'],
        ['/settings/connections/github', 'Add environment', 'Add environment'],
        ['/settings/connections/github', 'Disconnect GitHub…', 'Disconnect GitHub?'],
        ['/settings/connections/slack', 'Disconnect Slack…', 'Disconnect Slack?'],
        ['/settings/connections/mcp-docs', 'Review changes', 'Review changes to Docs'],
      ];
      for (const [route, button, name] of dialogs) {
        await go(page, route, route.endsWith('github') ? 'no-environments' : '');
        await page.getByRole('button', { name: button }).first().click();
        await page.getByRole('dialog', { name }).waitFor();
        await page.waitForTimeout(300);
        expect.soft(await axe(page), `${name} (${scheme})`).toEqual([]);
      }
    });
  }
});

test('plain copy on every Connections page', async ({ page }) => {
  for (const r of routes) {
    await go(page, r, '');
    await assertPlainCopy(page, { ignore: SHELL });
  }
  await go(page, '/settings/connections', 'channel-down:telegram,mcp-installing,github-none');
  await assertPlainCopy(page, { ignore: SHELL });
});

async function phone(browser: Browser, info: TestInfo) {
  return openPage(browser, info, viewports.phone, 'light');
}

test('fits a 360 px phone and keeps 44 px targets under a coarse pointer', async ({ browser }, info) => {
  const page = await phone(browser, info);
  try {
    for (const r of routes) {
      await go(page, r, '');
      await frame(page);
      expect.soft(await probe(page, { overflow: true, targets: true, minText: true, blur: false, shadows: false, nesting: false, leftEdge: false }), r).toEqual([]);
    }
    // The Telegram link step drops under its QR code; squeezed beside it, Open Telegram ran off the screen.
    await go(page, '/settings/connections/telegram', '');
    await page.getByRole('button', { name: 'Disconnect Telegram…' }).click();
    await page.getByRole('dialog', { name: 'Disconnect Telegram?' }).getByRole('button', { name: 'Disconnect Telegram', exact: true }).click();
    await page.getByLabel('Bot token').fill('123456789:' + 'AAH4kq9xV1t8mZp2Lq0wE3rTyUiOpAsDfGh');
    await page.getByLabel('Bot token').press('Enter');
    const open = page.getByRole('link', { name: /Open Telegram/ });
    await open.waitFor();
    const box = (await open.boundingBox())!;
    expect(box.x + box.width).toBeLessThanOrEqual(viewports.phone.width - 16 + 1);
  } finally {
    await page.context().close();
  }
});

test('the option lists and the people picker work from the keyboard', async ({ page }) => {
  await go(page, '/settings/connections/github', 'github-none,no-environments');
  const org = page.getByRole('radio', { name: 'An organization' });
  await org.focus();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('radio', { name: 'Your personal account' })).toBeChecked();
  await expect(page.getByLabel('Organization', { exact: true })).toHaveCount(0);
  await page.keyboard.press('ArrowUp');
  await expect(page.getByLabel('Organization', { exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Other ways to connect' }).click();
  const token = page.getByRole('radio', { name: 'Personal access token' });
  await token.focus();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('radio', { name: 'A GitHub App you already have' })).toBeChecked();
  await expect(page.getByRole('radio', { name: 'A GitHub App you already have' })).toBeFocused();
  await expect(page.getByLabel('App ID')).toBeVisible();
  await page.keyboard.press('ArrowUp');
  await expect(token).toBeChecked();
  await expect(page.getByLabel('Token', { exact: true })).toBeVisible();

  await go(page, '/settings/connections/github', 'no-environments');
  const add = page.getByRole('button', { name: 'Add environment' });
  await add.focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Add environment' });
  await expect(dialog.getByLabel('Name')).toBeFocused();
  const who = dialog.getByRole('combobox', { name: 'Who can approve' });
  await who.focus();
  await page.keyboard.type('Al');
  await expect(who).toHaveAttribute('aria-activedescendant', /.+/);
  await page.keyboard.press('Enter');
  await expect(dialog.getByText('Any admin, Alex')).toBeVisible();
  await page.keyboard.press('Backspace');
  await expect(dialog.getByText('Any admin, Alex')).toHaveCount(0);
  await expect(dialog.locator('p').filter({ hasText: /^Any admin$/ })).toBeVisible();
  // Esc closes the list first, then the dialog, and focus goes back to the button.
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Escape');
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(add).toBeFocused();
});

test('connect Slack: tokens, the check, then the link code', async ({ page }) => {
  await go(page, '/settings/connections/slack', 'paired:slack');
  await page.getByRole('button', { name: 'Disconnect Slack…' }).click();
  await page.getByRole('dialog', { name: 'Disconnect Slack?' }).getByRole('button', { name: 'Disconnect Slack' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Connect Slack' })).toBeVisible();
  await page.getByLabel('App-level token').fill('xapp-' + '1-A0123456789-1234567890123-abcdef');
  await page.getByLabel('Bot token').fill('xoxb-' + '1234567890-1234567890123-AbCdEfGhIjKlMnOp');
  await expect(page.getByText('Connected to Acme as @bothy.')).toBeVisible();
  await expect(page.getByText('/bothy pair 7KQ2-MX4D')).toBeVisible();
  await expect(page.getByText('Linked as @sam.')).toBeVisible();
  await go(page, '/settings/connections');
  await expect(page.getByText('Connected to Acme · 2 people linked')).toBeVisible();
});

test('connect GitHub: Bothy creates its app, then the repositories, then reading starts', async ({ page }) => {
  await go(page, '/settings/connections/github', 'github-none');
  await page.getByLabel('Organization', { exact: true }).fill('acme');
  // The mock's handoff page stands in for GitHub's create and install pages.
  await page.getByRole('button', { name: 'Continue on GitHub' }).click();
  const dialog = page.getByRole('dialog', { name: 'Which repositories can Bothy use?' });
  await dialog.getByLabel('acme/web').check();
  await dialog.getByRole('button', { name: 'Add repositories' }).click();
  await expect(page.getByText('Bothy is reading 1 repository.', { exact: false })).toBeVisible();
  await expect(page.getByText('Connected to acme as Bothy (acme)')).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/connections\/github$/);
  await expect(page.getByRole('switch', { name: 'Bothy can use acme/web' })).toBeChecked();
});

test('connect GitHub: a token, the repositories, then reading starts', async ({ page }) => {
  await go(page, '/settings/connections/github', 'github-none');
  await page.getByRole('button', { name: 'Other ways to connect' }).click();
  await page.getByLabel('Token', { exact: true }).fill('github_pat_11ABCDEFG0123456789abcdef');
  await expect(page.getByText('Connected as maya-acme. Bothy can see 6 repositories.')).toBeVisible();
  await page.getByLabel('acme/web').check();
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Bothy is reading 1 repository.', { exact: false })).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'GitHub' })).toBeVisible();
  await expect(page.getByRole('switch', { name: 'Bothy can use acme/web' })).toBeChecked();
});

test('add a deploy environment that needs two approvals', async ({ page }) => {
  await go(page, '/settings/connections/github', 'no-environments');
  await page.getByRole('button', { name: 'Add environment' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add environment' });
  await dialog.getByLabel('Name').fill('production');
  await dialog.getByRole('radio', { name: '2' }).click();
  await expect(dialog.getByText('Two different people must approve, and neither can be the person who asked.')).toBeVisible();
  await dialog.getByRole('button', { name: 'Save environment' }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByText('acme/api · Deploy workflow · 2 approvals from any admin')).toBeVisible();
});

test('web search: choosing SearXNG checks it and saves it', async ({ page }) => {
  await go(page, '/settings/connections/search', 'search-private');
  await page.getByRole('radio', { name: 'SearXNG' }).check();
  await page.getByRole('button', { name: 'Allow searxng:8080' }).click();
  await expect(page.getByText('Search works.')).toBeVisible();
  await go(page, '/settings/connections');
  await expect(page.getByRole('link', { name: 'Change Web search' })).toBeVisible();
  await expect(page.getByText('SearXNG')).toBeVisible();
});
