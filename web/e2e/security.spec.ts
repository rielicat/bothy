// C9 Settings: Security and Advanced (docs/design/ui.md §11.6, §11.7, §16):
// axe in both themes, plain copy, keyboard paths and touch targets.
import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';
import { openPage, probe, viewports } from './lib/shots';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

const routes = ['/settings/security', '/settings/security/permissions', '/settings/security/websites', '/settings/security/keys', '/settings/advanced'];

// The v0.1 shell (C1 replaces it) is not this chunk's to judge: copy checks
// skip it, and the probes run with it hidden, as the shot spec does.
const SHELL = ['nav[aria-label="Main"]', 'div:has(> #main) > header', '[role="region"][aria-label="Notices"]', '[role="dialog"][aria-label="Menu"]'];
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

async function frame(page: Page) {
  await page.evaluate((css) => {
    if (!document.querySelector('div:has(> #main) > header')) return;
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }, V02_FRAME);
}

function watch(page: Page) {
  const problems: string[] = [];
  page.on('console', (m) => m.type() === 'error' && /Content Security Policy|Refused to/.test(m.text()) && problems.push(m.text()));
  page.on('pageerror', (e) => problems.push(String(e)));
  page.on('request', (r) => !r.url().startsWith('http://localhost:') && !r.url().startsWith('data:') && !r.url().startsWith('blob:') && problems.push(`external request ${r.url()}`));
  return problems;
}

async function axe(page: Page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const r = await (window as unknown as { axe: { run: (n: Node) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run(document);
    return r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  });
}

async function go(page: Page, route: string, mock = '') {
  await page.goto(`${route}?mock=${mock}`);
  await page.waitForLoadState('networkidle');
  await page.locator('main h1').first().waitFor();
}

test.describe('axe', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`security and advanced pages and their dialogs (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of routes) {
        await go(page, r);
        await page.waitForTimeout(300);
        expect.soft(await axe(page), `${r} (${scheme})`).toEqual([]);
      }
      const dialogs: [string, string, (p: Page) => Promise<unknown>][] = [
        ['/settings/security', '', (p) => p.getByRole('button', { name: 'Pause Bothy…' }).click()],
        ['/settings/security/websites', '', (p) => p.getByRole('button', { name: 'Add website' }).click()],
        ['/settings/security/keys', '', (p) => p.getByRole('button', { name: 'Add key' }).click()],
        ['/settings/security/keys', 'sealed', (p) => p.getByRole('button', { name: 'Unlock', exact: true }).click()],
        ['/settings/advanced', '', (p) => p.getByRole('button', { name: 'Edit rule wiki' }).click()],
        ['/settings/advanced', '', (p) => p.getByRole('button', { name: 'Test a rule…' }).click()],
      ];
      for (const [r, mock, openIt] of dialogs) {
        await go(page, r, mock);
        await openIt(page);
        await page.getByRole('dialog').waitFor();
        await page.waitForTimeout(300);
        expect.soft(await axe(page), `${r} dialog (${scheme})`).toEqual([]);
      }
      await go(page, '/settings/security', 'sandbox-off');
      await page.getByRole('button', { name: 'How to turn it on' }).last().click();
      expect.soft(await axe(page), `running code off (${scheme})`).toEqual([]);
    });
  }
});

test('the main-flow pages speak plainly', async ({ page }) => {
  const problems = watch(page);
  for (const r of routes.filter((x) => !x.startsWith('/settings/advanced'))) {
    await go(page, r);
    await assertPlainCopy(page, { ignore: SHELL });
  }
  await go(page, '/settings/security', 'sandbox-off');
  await page.getByRole('button', { name: 'How to turn it on' }).last().click();
  await page.getByRole('button', { name: 'Technical details' }).click();
  await assertPlainCopy(page, { ignore: SHELL });
  for (const mock of ['paused', 'sealed']) {
    await go(page, mock === 'sealed' ? '/settings/security/keys' : '/settings/security', mock);
    await assertPlainCopy(page, { ignore: SHELL });
  }
  expect(problems).toEqual([]);
});

test('approvals settings work from the keyboard', async ({ page }) => {
  const problems = watch(page);
  await go(page, '/settings/security', 'no-findings');
  const balanced = page.getByRole('radio', { name: 'Balanced' });
  await balanced.focus();
  await page.keyboard.press('ArrowUp');
  await expect(page.getByRole('radio', { name: 'Careful' })).toBeChecked();
  await expect(page.getByRole('radio', { name: 'Careful' })).toBeFocused();
  await expect(page.getByText('Saved', { exact: true }).first()).toBeVisible();

  const chat = page.getByLabel('If nobody answers a chat request');
  await chat.focus();
  await chat.selectOption('park');
  await expect(chat).toHaveValue('park');

  // Pause: the dialog starts on Cancel, Esc closes it and focus goes back.
  const pause = page.getByRole('button', { name: 'Pause Bothy…' });
  await pause.focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Pause Bothy?' });
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(pause).toBeFocused();
  await page.keyboard.press('Enter');
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('button', { name: 'Pause Bothy', exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('main').getByRole('button', { name: 'Resume' })).toBeVisible();
  await page.getByRole('main').getByRole('button', { name: 'Resume' }).press('Enter');
  await expect(page.getByRole('button', { name: 'Pause Bothy…' })).toBeVisible();
  expect(problems).toEqual([]);
});

test('saved keys: add, unlock and delete without a pointer', async ({ page }) => {
  const problems = watch(page);
  await go(page, '/settings/security/keys');
  await page.getByRole('button', { name: 'Add key' }).press('Enter');
  const add = page.getByRole('dialog', { name: 'Add key' });
  await expect(add.getByLabel('Name')).toBeFocused();
  await page.keyboard.type('Tavily API key');
  await page.keyboard.press('Tab');
  await page.keyboard.type('tvly-1234');
  await page.keyboard.press('Enter');
  await expect(page.getByText('Tavily API key')).toBeVisible();
  await expect(add).toBeHidden();

  const more = page.getByRole('button', { name: 'More for Tavily API key' });
  await more.focus();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'Delete…' })).toBeFocused();
  await page.keyboard.press('Enter');
  const del = page.getByRole('dialog', { name: 'Delete Tavily API key?' });
  await expect(del.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Enter');
  await expect(page.getByText('Tavily API key')).toHaveCount(0);

  await go(page, '/settings/security/keys', 'sealed');
  await page.getByRole('button', { name: 'Unlock', exact: true }).press('Enter');
  const unlock = page.getByRole('dialog', { name: 'Unlock saved keys' });
  await expect(unlock.getByLabel('Passphrase')).toBeFocused();
  await page.keyboard.type('wrong');
  await page.keyboard.press('Enter');
  await expect(unlock.getByText("That passphrase didn't work. Check it and try again.")).toBeVisible();
  await expect(unlock.getByLabel('Passphrase')).toBeFocused();
  expect(problems).toEqual([]);
});

test('advanced: provenance only where it belongs, and reset works', async ({ page }) => {
  await go(page, '/settings/advanced');
  await expect(page.getByText('Set by the server environment (BOTHY_LOG__LEVEL). Change it there.')).toBeVisible();
  await expect(page.getByLabel('Log level')).toBeDisabled();
  const memory = page.getByLabel('Memory per workspace (MB)');
  await expect(memory).toHaveValue('1024');
  await page.getByRole('button', { name: 'Reset to default' }).first().click();
  await expect(memory).toHaveValue('512');
  // Provenance never shows on the main-flow Security page.
  await go(page, '/settings/security');
  await expect(page.getByText(/Changed ·|Set by the server environment/)).toHaveCount(0);
});

test('members are told the pages are for admins', async ({ page }) => {
  for (const r of routes) {
    await go(page, r, 'member');
    await expect(page.getByText('Only admins can see this page.'), r).toBeVisible();
  }
});

test('every control is a 44 px target on a touch phone, nothing scrolls sideways', async ({ browser }, info) => {
  const page = await openPage(browser, info, viewports.phone, 'light');
  try {
    for (const r of routes) {
      await go(page, r);
      await frame(page);
      expect(await probe(page, { overflow: true, targets: true, minText: true }), r).toEqual([]);
    }
  } finally {
    await page.context().close();
  }
});
