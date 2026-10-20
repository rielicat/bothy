// C5 Knowledge (docs/design/ui.md §9, §18.3) against the production build:
// axe in both themes with the dialogs open, plain copy on the main flow,
// monospace only for code and paths, and the keyboard paths through notes,
// search, memory and suggestions. Every page opens with ?mock= so the fixture
// gateway starts clean.
import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

const PROJECT = '/knowledge/projects/acme/web';
const routes = ['/knowledge', PROJECT, '/knowledge/projects/acme/api', '/knowledge/projects/acme/infra', '/knowledge/search?q=login%20redirect', '/knowledge/memory', '/knowledge/skills', '/knowledge/skills/release-notes', '/knowledge/skills/incident-review'];

const open = (page: Page, path: string, mock = '') => page.goto(`${path}${path.includes('?') ? '&' : '?'}mock=${mock}`);

function watch(page: Page) {
  const problems: string[] = [];
  page.on('console', (m) => m.type() === 'error' && /Content Security Policy|Refused to/.test(m.text()) && problems.push(m.text()));
  page.on('pageerror', (e) => problems.push(String(e)));
  return problems;
}

// `skip` leaves out what another chunk owns and checks (the approval card is C4's).
async function axe(page: Page, skip: string[] = []) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async (exclude) => {
    type Axe = { run: (n: unknown) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> };
    const r = await (window as unknown as { axe: Axe }).axe.run(exclude.length ? { include: [['body']], exclude: exclude.map((s) => [s]) } : document);
    return r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  }, skip);
}

/** Text set in a monospace face outside code, paths and commits (§3.2: mono only for code and paths). */
function stray(page: Page) {
  return page.evaluate(() => {
    const out: string[] = [];
    const walker = document.createTreeWalker(document.querySelector('main') ?? document.body, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const el = n.parentElement;
      if (!el || !n.textContent?.trim() || !el.checkVisibility()) continue;
      if (/monospace|Menlo|Consolas|SFMono/i.test(getComputedStyle(el).fontFamily) && !el.closest('pre, code, [data-path]')) out.push(n.textContent.trim().slice(0, 40));
    }
    return out;
  });
}

test.describe('knowledge axe', () => {
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`routes and dialogs are clean (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of routes) {
        await open(page, r);
        await page.waitForLoadState('networkidle');
        expect.soft(await axe(page), `${r} (${scheme})`).toEqual([]);
      }
      await open(page, PROJECT, 'notes-suggestions');
      await page.getByRole('button', { name: 'Review' }).click();
      await page.waitForTimeout(300);
      expect.soft(await axe(page, ['[data-approval]']), `suggestions (${scheme})`).toEqual([]);
      await page.keyboard.press('Escape');
      await page.getByRole('button', { name: 'History' }).click();
      await page.getByRole('dialog', { name: 'History' }).getByRole('button', { name: 'Show' }).first().click();
      await page.waitForTimeout(300);
      expect.soft(await axe(page), `history (${scheme})`).toEqual([]);
      await page.keyboard.press('Escape');
      await page.getByRole('button', { name: 'Edit' }).click();
      expect.soft(await axe(page), `editing (${scheme})`).toEqual([]);
    });
  }
});

test('the main flow reads plainly, as admin and as member', async ({ page }) => {
  const problems = watch(page);
  for (const mock of ['', 'member']) {
    for (const r of routes) {
      await open(page, r, mock);
      await page.waitForLoadState('networkidle');
      await assertPlainCopy(page);
      expect(await stray(page), `monospace prose on ${r}`).toEqual([]);
    }
  }
  expect(problems).toEqual([]);
});

test('edit notes from the keyboard and save with ⌘S / Ctrl S', async ({ page }) => {
  const problems = watch(page);
  await open(page, PROJECT);
  const edit = page.getByRole('button', { name: 'Edit' });
  await edit.focus();
  await page.keyboard.press('Enter');
  const box = page.getByRole('textbox', { name: 'Notes for acme/web' });
  await expect(box).toBeFocused();
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.type('\n- Keep pull requests small.');
  await page.keyboard.press('ControlOrMeta+s');
  await expect(page.getByRole('status').filter({ hasText: 'Saved.' })).toBeVisible();
  await expect(page.getByText('Keep pull requests small.')).toBeVisible();
  await expect(box).toHaveCount(0);
  expect(problems).toEqual([]);
});

test('a conflicting save keeps the edit until the person chooses', async ({ page }) => {
  await open(page, PROJECT, 'notes-conflict');
  await page.getByRole('button', { name: 'Edit' }).click();
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.type('\n- Mine.');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Someone else changed these notes while you were editing.')).toBeVisible();
  await page.getByRole('button', { name: 'Compare versions' }).click();
  const dialog = page.getByRole('dialog', { name: 'Compare versions' });
  await expect(dialog.getByRole('button', { name: 'Use their version' })).toBeVisible();
  await dialog.getByRole('button', { name: 'Keep mine' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Saved.' })).toBeVisible();
  // Keeping mine replaces their version with this edit.
  await expect(page.getByText('Mine.')).toBeVisible();
  await expect(page.getByText('shared date helpers')).toHaveCount(0);
});

test('search from the Knowledge field, then narrow it', async ({ page }) => {
  await open(page, '/knowledge');
  const field = page.getByRole('searchbox', { name: 'Search code and notes' });
  await field.fill('login redirect');
  await field.press('Enter');
  await expect(page).toHaveURL(/\/knowledge\/search\?q=login\+redirect|\/knowledge\/search\?q=login%20redirect/);
  await expect(page.getByText('src/auth/redirect.ts', { exact: true })).toBeVisible();
  await expect(page.locator('mark').first()).toBeVisible();
  await page.getByRole('radio', { name: 'Notes' }).click();
  await expect(page.getByText('src/auth/redirect.ts', { exact: true })).toHaveCount(0);
  await expect(page.getByText(/Project notes/)).toBeVisible();
  await page.getByRole('radio', { name: 'All' }).click();
  await page.getByLabel('In', { exact: true }).selectOption('acme/docs');
  await expect(page.getByText('src/content/sign-in.md', { exact: true })).toBeVisible();
  await expect(page.getByText('src/auth/redirect.ts', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Ask Bothy about “login redirect”' })).toBeVisible();
});

test('memory saves and says when it applies', async ({ page }) => {
  await open(page, '/knowledge/memory');
  const about = page.getByRole('region', { name: 'About you' });
  await about.getByRole('button', { name: 'Edit' }).click();
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.type(' Based in Lisbon.');
  await about.getByRole('button', { name: 'Save' }).click();
  await expect(about.getByRole('status')).toHaveText('Saved');
  await expect(about.getByRole('status')).toHaveText('Changes apply from your next message.', { timeout: 5000 });
  await expect(about.getByText(/Based in Lisbon/)).toBeVisible();
});

test("an admin changes what's remembered for everyone; a member only reads it", async ({ page }) => {
  await open(page, '/knowledge/memory');
  const team = page.getByRole('region', { name: 'Remembered for everyone' });
  await team.getByRole('button', { name: 'Edit' }).click();
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.type('\nThe office is closed on Fridays.');
  await team.getByRole('button', { name: 'Save' }).click();
  await expect(team.getByRole('status')).toHaveText("Changes apply from everyone's next message.", { timeout: 5000 });
  await open(page, '/knowledge/memory', 'member');
  await expect(team.getByText('Only admins can change it.')).toBeVisible();
  await expect(team.getByRole('button')).toHaveCount(0);
});

test('approving the last suggestion closes the review and the notice', async ({ page }) => {
  await open(page, PROJECT, 'notes-suggestions');
  await page.getByRole('button', { name: 'Review' }).click();
  const dialog = page.getByRole('dialog', { name: 'Suggested changes' });
  await expect(dialog.getByRole('article')).toHaveCount(2);
  for (let i = 0; i < 2; i++) await dialog.getByRole('article').first().getByRole('button', { name: 'Update notes' }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText(/Bothy suggested/)).toHaveCount(0);
});

// A row's stretched link rings the whole row; a control of its own on that row rings only itself, so focus is never shown twice.
test('tabbing along a skill row rings the row for its link and the Review button on its own', async ({ page }) => {
  await open(page, '/knowledge/skills');
  const link = page.getByRole('link', { name: /^Incident review/ });
  const row = page.locator('.row-stretch', { has: link });
  await link.focus();
  await page.keyboard.press('Shift+Tab');
  await page.keyboard.press('Tab');
  await expect(link).toBeFocused();
  await expect(row).toHaveCSS('outline-style', 'solid');
  await page.keyboard.press('Tab');
  await expect(row.getByRole('link', { name: 'Review', exact: true })).toBeFocused();
  await expect(row).toHaveCSS('outline-style', 'none');
});
