// F2 primitives (docs/design/ui.md §18.3): the /dev/ui gallery in every state,
// axe on it in both themes, the keyboard paths of the overlays, and a check
// that production builds leave the gallery out.
//
// The gallery exists only in dev builds, so its shots and checks run against
// the mock dev server:
//
//   npm run mock -- --port 5173 &
//   SHOTS=1 SHOTS_BASE_URL=http://localhost:5173 npx playwright test e2e/shots/primitives.spec.ts
//
// The production check runs in the normal smoke run (npm run build && npm run smoke).
import { expect, test, type Page } from '@playwright/test';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';
import { assertPlainCopy } from '../lib/copy';
import { openPage, shots, themes, viewports, type ShotState, type ViewportName } from '../lib/shots';

const axePath = fileURLToPath(new URL('../../node_modules/axe-core/axe.min.js', import.meta.url));
const dist = fileURLToPath(new URL('../../../internal/webui/dist/assets/', import.meta.url));

// Until C1 lands, the gallery renders inside the v0.1 shell: a 240 px sidebar,
// a blurred top bar and a main area with its own scroll. This puts the gallery
// in the v0.2 frame instead (document scroll, content edge after a 256 px
// sidebar), so the captures and probes judge the primitives, not the old shell.
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

async function frame(page: Page) {
  await page.addStyleTag({ content: V02_FRAME });
  await page.getByRole('heading', { level: 1, name: 'UI gallery' }).waitFor();
}

const click = (name: string | RegExp) => async (page: Page) => {
  await frame(page);
  await page.getByRole('button', { name }).first().click();
};

const states: ShotState[] = [
  { name: 'gallery', route: '/dev/ui', setup: frame, full: true },
  { name: 'menu', route: '/dev/ui', setup: click('Chat options'), full: false },
  { name: 'menu-theme', route: '/dev/ui', setup: click('Gallery options'), full: false },
  { name: 'listbox', route: '/dev/ui', setup: click(/^Model:/), full: false },
  {
    name: 'combobox',
    route: '/dev/ui',
    full: false,
    setup: async (page) => {
      await frame(page);
      const input = page.locator('input[role=combobox]').and(page.getByLabel('Time zone'));
      await input.scrollIntoViewIfNeeded();
      await input.click();
      await input.fill('o');
    },
  },
  {
    name: 'combobox-none',
    route: '/dev/ui',
    full: false,
    setup: async (page) => {
      await frame(page);
      const input = page.locator('input[role=combobox]').and(page.getByLabel('Approvers'));
      await input.scrollIntoViewIfNeeded();
      await input.click();
      await input.fill('zz');
    },
  },
  { name: 'dialog-confirm', route: '/dev/ui', setup: click('Open confirm dialog'), full: false },
  {
    name: 'dialog-form',
    route: '/dev/ui',
    full: false,
    setup: async (page) => {
      await click('Open form dialog')(page);
      await page.getByRole('dialog').getByLabel('Username').fill('alex kim');
    },
  },
  { name: 'dialog-review', route: '/dev/ui', setup: click('Open review dialog'), full: false },
  { name: 'drawer', route: '/dev/ui', setup: click('Open drawer'), full: false },
  { name: 'drawer-left', route: '/dev/ui', setup: click('Open left drawer'), full: false },
  {
    name: 'toast',
    route: '/dev/ui',
    full: false,
    setup: async (page) => {
      await click('Show a toast with an action')(page);
      await page.mouse.move(0, 0);
    },
  },
  {
    name: 'disclosures-open',
    route: '/dev/ui',
    full: false,
    setup: async (page) => {
      await frame(page);
      const section = page.locator('#navigation');
      for (const name of ['Details', 'Show', 'More options']) await section.getByRole('button', { name, exact: true }).click();
      await section.getByRole('button', { name: 'Details', exact: true }).scrollIntoViewIfNeeded();
      await page.waitForTimeout(300);
    },
  },
  {
    name: 'tooltip',
    route: '/dev/ui',
    full: false,
    only: { viewports: ['desktop'] },
    setup: async (page) => {
      await frame(page);
      await page.getByRole('button', { name: 'Search', exact: true }).first().hover();
      await page.getByRole('tooltip').waitFor();
    },
  },
];

shots('primitives', states);

async function axe(page: Page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const r = await (window as unknown as { axe: { run: (n: Node) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run(document);
    return r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  });
}

test.describe('gallery checks (dev server)', () => {
  test.skip(!process.env.SHOTS || !process.env.SHOTS_BASE_URL, 'needs SHOTS=1 and the dev server in SHOTS_BASE_URL');

  for (const vp of Object.keys(viewports) as ViewportName[]) {
    for (const theme of themes) {
      test(`axe is clean at ${viewports[vp].name} in ${theme}`, async ({ browser }, info) => {
        const page = await openPage(browser, info, viewports[vp], theme);
        try {
          await page.goto('/dev/ui?mock=');
          await frame(page);
          await page.waitForTimeout(300);
          expect(await axe(page), 'gallery').toEqual([]);
          await assertPlainCopy(page);
          // Each overlay is checked after its entry transition: contrast is measured on what is painted.
          for (const name of ['Open confirm dialog', 'Open form dialog', 'Open review dialog', 'Open drawer']) {
            await page.getByRole('button', { name }).click();
            await page.waitForTimeout(300);
            expect(await axe(page), name).toEqual([]);
            await page.keyboard.press('Escape');
          }
          await page.getByRole('button', { name: 'Chat options' }).first().click();
          await page.waitForTimeout(200);
          expect(await axe(page), 'menu').toEqual([]);
          await page.keyboard.press('Escape');
          await page.getByRole('button', { name: /^Model:/ }).click();
          await page.waitForTimeout(200);
          expect(await axe(page), 'listbox').toEqual([]);
          await page.keyboard.press('Escape');
        } finally {
          await page.context().close();
        }
      });
    }
  }

  test('overlays work from the keyboard and give focus back', async ({ browser }, info) => {
    const page = await openPage(browser, info, viewports.desktop, 'light');
    try {
      await page.goto('/dev/ui?mock=');
      await frame(page);

      const confirm = page.getByRole('button', { name: 'Open confirm dialog' });
      await confirm.focus();
      await page.keyboard.press('Enter');
      const dialog = page.getByRole('dialog', { name: 'Delete notes/staging.md?' });
      await expect(dialog).toBeVisible();
      await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
      await page.keyboard.press('Shift+Tab');
      await page.keyboard.press('Shift+Tab');
      expect(await page.evaluate(() => !!document.activeElement?.closest('dialog'))).toBe(true);
      await page.keyboard.press('Escape');
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(confirm).toBeFocused();

      const form = page.getByRole('button', { name: 'Open form dialog' });
      await form.focus();
      await page.keyboard.press('Enter');
      await expect(page.getByRole('dialog').getByLabel('Name', { exact: true })).toBeFocused();
      await page.keyboard.type('Sam');
      await page.mouse.click(5, 5);
      await expect(page.getByRole('dialog', { name: 'Add person' })).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(form).toBeFocused();

      const more = page.getByRole('button', { name: 'Chat options' }).first();
      await more.focus();
      await page.keyboard.press('ArrowDown');
      await expect(page.getByRole('menuitem', { name: 'Rename' })).toBeFocused();
      await page.keyboard.press('d');
      await expect(page.getByRole('menuitem', { name: 'Delete chat…' })).toBeFocused();
      await page.keyboard.press('Escape');
      await expect(page.getByRole('menu')).toHaveCount(0);
      await expect(more).toBeFocused();

      const picker = page.getByRole('button', { name: /^Model:/ });
      await picker.focus();
      await page.keyboard.press('ArrowDown');
      const list = page.getByRole('listbox', { name: 'Model' });
      await expect(list).toBeFocused();
      await page.keyboard.press('ArrowDown');
      await page.keyboard.press('Enter');
      await expect(picker).toHaveAccessibleName('Model: Claude Haiku 4.5');
      await expect(picker).toBeFocused();

      const zone = page.locator('input[role=combobox]').and(page.getByLabel('Time zone'));
      await zone.fill('tok');
      await expect(zone).toHaveAttribute('aria-activedescendant', /.+/);
      await page.keyboard.press('Enter');
      await expect(zone).toHaveValue('Tokyo');
    } finally {
      await page.context().close();
    }
  });
});

test('production builds leave the gallery out', async ({ page }) => {
  test.skip(!!process.env.SHOTS_BASE_URL, 'checks the production preview');
  await page.goto('/dev/ui');
  await expect(page.getByText('Check the address, or go back to your chats.')).toBeVisible();
  const scripts = readdirSync(dist).filter((f) => f.endsWith('.js.gz'));
  expect(scripts.length).toBeGreaterThan(0);
  for (const f of scripts) expect(gunzipSync(readFileSync(join(dist, f))).toString('utf8'), f).not.toContain('UI gallery');
});
