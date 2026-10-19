// C7 Settings: Account, General, Models, People (docs/design/ui.md §18.3).
// axe in both themes, plain copy and the keyboard paths, against the
// production build under the gateway's CSP. Every page opens with ?mock= so
// it starts from fresh fixtures.
import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';
import { probe } from './lib/shots';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

const routes = ['/settings/account', '/settings/general', '/settings/general/personality', '/settings/models', '/settings/people'];

// The v0.1 shell around these pages until C1 lands; its own copy, axe results
// and touch targets are C1's. The frame hides it the way the shot spec does.
const shell = ['nav[aria-label="Main"]', 'header', '[aria-label="Notices"]'];
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
`;

async function axe(page: Page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const run = (window as unknown as { axe: { run: (n: Node) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run;
    const r = await run(document.querySelector('#main') ?? document);
    return r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  });
}

async function go(page: Page, route: string, mock = '') {
  await page.goto(`${route}?mock=${mock}`);
  await page.waitForLoadState('networkidle');
  await page.locator('h1').first().waitFor();
}

// A same-origin stylesheet, so the gateway's CSP (style-src 'self') lets it in.
async function framed(page: Page, route: string, mock = '') {
  await go(page, route, mock);
  await page.route('**/__v02-frame.css', (r) => r.fulfill({ contentType: 'text/css', body: V02_FRAME }));
  await page.addStyleTag({ url: '/__v02-frame.css' });
}

test.describe('settings: axe and plain copy', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });

  for (const scheme of ['light', 'dark'] as const) {
    test(`every page and dialog passes axe (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of routes) {
        await go(page, r, 'provider-bedrock');
        await page.waitForTimeout(300);
        expect.soft(await axe(page), `${r} (${scheme})`).toEqual([]);
      }
      const dialogs: [string, string][] = [
        ['/settings/account', 'Change password…'],
        ['/settings/account', 'Create token'],
        ['/settings/models', 'Add a provider'],
        ['/settings/models', 'Manage Amazon Bedrock'],
        ['/settings/people', 'Add person'],
      ];
      for (const [r, name] of dialogs) {
        await go(page, r, 'provider-bedrock');
        await page.getByRole('button', { name, exact: true }).first().click();
        await page.getByRole('dialog').waitFor();
        await page.waitForTimeout(400);
        expect.soft(await axe(page), `${r} → ${name} (${scheme})`).toEqual([]);
      }
      await go(page, '/settings/models', 'provider-bedrock');
      await page.getByRole('button', { name: 'Change main model' }).click();
      await page.waitForTimeout(400);
      expect.soft(await axe(page), `/settings/models → Change (${scheme})`).toEqual([]);
    });
  }

  test('the words follow the copy guide', async ({ page }) => {
    for (const r of routes) {
      await go(page, r, 'provider-bedrock');
      await assertPlainCopy(page, { ignore: shell });
    }
    await go(page, '/settings/account', 'member');
    await assertPlainCopy(page, { ignore: shell });
    await go(page, '/settings/people', 'people-solo');
    await assertPlainCopy(page, { ignore: shell });
  });
});

test('no page scrolls sideways at 360 px', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  for (const r of routes) {
    await framed(page, r, 'provider-bedrock');
    expect(await probe(page, { overflow: true, leftEdge: false, minText: true, blur: false, shadows: false, nesting: false, targets: false }), r).toEqual([]);
  }
});

test('change the main model from the keyboard alone, and focus comes back', async ({ page }) => {
  await go(page, '/settings/models');
  const change = page.getByRole('button', { name: 'Change main model' });
  await change.focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Main model' });
  const list = dialog.getByRole('listbox');
  await expect(list).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText('Claude Haiku 4.5 · Anthropic').first()).toBeVisible();
  await expect(change).toBeFocused();

  // Esc closes without saving and also gives focus back.
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog', { name: 'Main model' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(change).toBeFocused();
});

test('a setting saves when the field loses focus and says so', async ({ page }) => {
  await go(page, '/settings/general');
  const limit = page.getByLabel('Daily limit');
  await limit.fill('12');
  await page.keyboard.press('Tab');
  await expect(page.getByText('Saved', { exact: true })).toBeVisible();
  await expect(limit).toHaveValue('12.00');
  // Without ?mock=, which would reset the fixtures.
  await page.goto('/settings/general');
  await expect(page.getByLabel('Daily limit')).toHaveValue('12.00');
});

test("change Bothy's picture, see it under the CSP, then go back to the default", async ({ page }) => {
  await go(page, '/settings/general');
  const img = page.getByRole('img', { name: 'Bothy' });
  const shown = () => img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0);
  await expect.poll(shown).toBe(true);
  const before = await img.getAttribute('src');
  // The mock shows a stand-in for bytes that aren't a picture.
  await page.locator('input[type="file"]').setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: Buffer.from('stand-in') });
  await expect(page.getByText('Picture changed.')).toBeVisible();
  await expect(img).not.toHaveAttribute('src', before ?? '');
  await expect.poll(shown).toBe(true);
  await page.getByRole('button', { name: 'Use default' }).click();
  await expect(page.getByText('Default picture restored.')).toBeVisible();
  // Telegram is on, so the ghost button stays as "Send to chat apps" and keeps focus.
  await expect(page.getByRole('button', { name: 'Send to chat apps' })).toBeFocused();
});

test('create a token from the keyboard and see it once', async ({ page }) => {
  await go(page, '/settings/account');
  await page.getByRole('button', { name: 'Create token' }).focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Create token' });
  await expect(dialog.getByLabel('Name')).toBeFocused();
  await page.keyboard.type('Laptop');
  await page.keyboard.press('Enter');
  const result = page.getByRole('dialog', { name: 'Copy your token' });
  await expect(result.getByText(/^bothy_pat_/)).toBeVisible();
  await expect(result.getByRole('button', { name: /^Copy/ }).first()).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.getByText('Laptop')).toBeVisible();
  await expect(page.getByText(/^bothy_pat_/)).toHaveCount(0);
});

test('Account connects and disconnects sign-in methods', async ({ page }) => {
  await go(page, '/settings/account', 'signin-linked,signin-github');
  const section = page.locator('section', { has: page.getByRole('heading', { name: 'Sign-in methods' }) });
  await expect(section.getByText('sam@acme.com')).toBeVisible();
  await section.getByRole('button', { name: 'Disconnect Google' }).click();
  await expect(page.getByText('Google disconnected.')).toBeVisible();
  await expect(section.getByRole('button', { name: 'Connect Google' })).toBeVisible();
  // Connecting goes through the provider and comes back here.
  await section.getByRole('button', { name: 'Connect GitHub' }).click();
  await page.waitForURL('**/settings/account');
  await expect(page.getByText('GitHub connected.')).toBeVisible();
  await expect(section.getByText('@sam-acme')).toBeVisible();
  await assertPlainCopy(page, { ignore: shell });
});

test('people in chat apps: can approve, daily limit, block and delete', async ({ page }) => {
  await go(page, '/settings/people');
  const section = page.locator('section', { has: page.getByRole('heading', { name: 'In chat apps' }) });
  const alex = section.getByRole('switch', { name: 'Can approve: Alex Kim' });
  await alex.click();
  await expect(alex).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByText('Saved.')).toBeVisible();

  await section.getByRole('button', { name: 'Options for Alex Kim' }).click();
  await page.getByRole('menuitem', { name: 'Daily limit…' }).click();
  const limit = page.getByRole('dialog', { name: 'Daily limit for Alex Kim' });
  await limit.getByRole('radio', { name: 'Set a limit' }).check();
  await limit.getByRole('textbox', { name: 'Daily limit' }).fill('5');
  await limit.getByRole('button', { name: 'Save' }).click();
  await expect(section.getByText(/of \$5\.00 today/)).toBeVisible();

  await section.getByRole('button', { name: 'Options for Morgan Reyes' }).click();
  await page.getByRole('menuitem', { name: 'Block…' }).click();
  await page.getByRole('dialog', { name: 'Block Morgan Reyes?' }).getByRole('button', { name: 'Block' }).click();
  await expect(section.getByText('Discord · Blocked')).toBeVisible();

  await section.getByRole('button', { name: 'Options for Morgan Reyes' }).click();
  await page.getByRole('menuitem', { name: 'Delete their data…' }).click();
  const del = page.getByRole('dialog', { name: "Delete Morgan Reyes's data?" });
  await expect(del.getByText('They stay blocked.', { exact: false })).toBeVisible();
  await del.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Deleted.')).toBeVisible();
  await assertPlainCopy(page, { ignore: shell });
});

test('members see only their account', async ({ page }) => {
  await go(page, '/settings/account', 'member');
  await expect(page.getByRole('heading', { level: 1, name: 'Account' })).toBeVisible();
  for (const r of ['/settings/general', '/settings/models', '/settings/people']) {
    await page.goto(r);
    await expect(page.getByText('Only admins can see this page.'), r).toBeVisible();
  }
});

test.describe('on a touch phone', () => {
  test.use({ viewport: { width: 360, height: 780 }, hasTouch: true, isMobile: true });

  test('controls are at least 44 px and groups link back to Settings', async ({ page }) => {
    for (const r of routes) {
      await framed(page, r, 'provider-bedrock');
      expect(await probe(page, { overflow: false, leftEdge: false, minText: false, blur: false, shadows: false, nesting: false, targets: true }), r).toEqual([]);
    }
    await go(page, '/settings/models');
    await expect(page.getByRole('link', { name: 'Settings' })).toBeVisible();
    await go(page, '/settings/general/personality');
    await expect(page.getByRole('link', { name: 'General' })).toBeVisible();
  });
});
