// C6 Activity in the production build (docs/design/ui.md §10, §16, §18.2):
// axe with contrast in both themes, plain copy, the keyboard paths of the
// chart, menus and dialogs, and touch-sized targets on a phone.
import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';
import { openPage, probe, viewports } from './lib/shots';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

const routes: [string, string][] = [
  ['/activity', 'Fix login redirect'],
  ['/activity?mock=member', 'Draft the release notes'],
  ['/activity/tasks/run_01jfixlogin0000000000000', 'What Bothy did'],
  ['/activity/tasks/run_01jpricing00000000000000', 'Refined from'],
  ['/activity/tasks/run_missing', 'This task was deleted, or it belongs to someone else.'],
  ['/activity/schedules', 'Weekly PR digest'],
  ['/activity/schedules?mock=activity-empty', 'Monday pull request summary'],
  ['/activity/schedules/new', 'What should Bothy do?'],
  ['/activity/schedules/new?example=pr-summary&mock=activity-empty', 'What should Bothy do?'],
  ['/activity/schedules/sch_01jstandup00000000000000', 'Recent runs'],
  ['/activity/schedules/checkin', 'What to check'],
  ['/activity/schedules/checkin?mock=checkin-empty', 'Use suggested lines'],
  ['/activity/releases?mock=deploy-waiting', 'History'],
  ['/activity/releases?mock=releases-none', 'No deploy environments yet.'],
  ['/activity/spending', 'By model'],
  ['/activity/spending?mock=spend-limit', "Bothy reached today's limit."],
  ['/activity/audit', 'Approved deleting'],
  ['/activity/audit?view=web', 'Reached api.github.com'],
];

const withMock = (r: string) => (r.includes('mock=') ? r : `${r}${r.includes('?') ? '&' : '?'}mock=`);

async function open(page: Page, route: string, ready: string) {
  await page.goto(withMock(route));
  // In main: the shell's chat list repeats some of these titles, hidden in the drawer on phones.
  await page.getByRole('main').getByText(ready).first().waitFor();
  await page.waitForLoadState('networkidle');
}

async function axe(page: Page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const r = await (window as unknown as { axe: { run: (n: Node) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run(document);
    return r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
  });
}

test.describe('axe', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`Activity routes and overlays (${scheme})`, async ({ page }) => {
      test.setTimeout(120_000);
      await page.emulateMedia({ colorScheme: scheme });
      for (const [r, ready] of routes) {
        await open(page, r, ready);
        expect.soft(await axe(page), `${r} (${scheme})`).toEqual([]);
      }
      await open(page, '/activity/tasks/run_01jfixlogin0000000000000', 'What Bothy did');
      await page.getByRole('button', { name: /^Changed/ }).click();
      await page.getByRole('button', { name: 'Technical details' }).click();
      // Contrast is measured on what is painted, so after the disclosure's fade-in.
      await page.waitForTimeout(300);
      expect.soft(await axe(page), `task details open (${scheme})`).toEqual([]);
      await open(page, '/activity/schedules/new', 'What should Bothy do?');
      await page.getByRole('button', { name: 'More options' }).click();
      await page.getByRole('button', { name: 'Create schedule' }).click();
      await page.waitForTimeout(300);
      expect.soft(await axe(page), `schedule errors (${scheme})`).toEqual([]);
      await open(page, '/activity/audit?view=web', 'Reached api.github.com');
      await page.getByRole('button', { name: 'Allow this site…' }).click();
      await page.waitForTimeout(250);
      expect.soft(await axe(page), `allow site dialog (${scheme})`).toEqual([]);
    });
  }
});

test('main-flow copy is plain on every Activity route', async ({ page }) => {
  test.setTimeout(120_000);
  for (const [r, ready] of routes) {
    await open(page, r, ready);
    await assertPlainCopy(page);
  }
});

test('chart bars, menus and dialogs work from the keyboard', async ({ page }) => {
  await open(page, '/activity/spending?range=7', 'in the last 7 days');
  const bar = page.getByRole('img').first();
  await bar.focus();
  await expect(page.getByRole('tooltip')).toHaveText(await bar.getAttribute('aria-label') as string);
  await page.keyboard.press('Tab');
  await expect(page.getByRole('img').nth(1)).toBeFocused();

  await open(page, '/activity/schedules', 'Weekly PR digest');
  const more = page.getByRole('button', { name: 'More for Weekly PR digest' });
  await more.focus();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'Run now' })).toBeFocused();
  await page.keyboard.press('End');
  await expect(page.getByRole('menuitem', { name: 'Delete…' })).toBeFocused();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Delete the schedule “Weekly PR digest”?' });
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(more).toBeFocused();

  await open(page, '/activity/audit?view=web', 'Reached api.github.com');
  const allow = page.getByRole('button', { name: 'Allow this site…' });
  await allow.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog', { name: 'Allow Bothy to send data to paste.example.net?' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(allow).toBeFocused();

  await open(page, '/activity/audit', 'Approved deleting');
  const row = page.getByRole('button', { name: /Approved deleting/ });
  await row.focus();
  await page.keyboard.press('Enter');
  await expect(row).toHaveAttribute('aria-expanded', 'true');
});

test('a schedule can be created end to end', async ({ page }) => {
  await open(page, '/activity/schedules/new', 'What should Bothy do?');
  await page.getByRole('button', { name: 'Create schedule' }).click();
  await expect(page.getByText('Tell Bothy what to do.')).toBeVisible();
  await page.getByLabel('What should Bothy do?').fill('List the pull requests that waited more than two days for review.');
  await page.getByLabel('Repeat', { exact: true }).selectOption('weekly');
  await expect(page.getByText(/^Runs Mondays at /)).toBeVisible();
  await page.getByRole('button', { name: 'Create schedule' }).click();
  await expect(page).toHaveURL(/\/activity\/schedules$/);
  await expect(page.getByRole('link', { name: 'List the pull requests that waited more than' })).toBeVisible();
});

test('a schedule can be created from an example', async ({ page }) => {
  await open(page, '/activity/schedules?mock=activity-empty', 'Examples');
  await page.getByRole('link', { name: /^Monday pull request summary/ }).click();
  await expect(page).toHaveURL(/\/activity\/schedules\/new\?example=pr-summary$/);
  await expect(page.getByLabel('What should Bothy do?')).toHaveValue(/^Summarise the pull requests in acme\/web from the last 7 days/);
  await expect(page.getByRole('textbox', { name: 'Name' })).toHaveValue('Monday pull request summary');
  await expect(page.getByText(/^Runs Mondays at .+ Each result gets its own chat in your list\./)).toBeVisible();
  await page.getByRole('button', { name: 'Create schedule' }).click();
  await expect(page).toHaveURL(/\/activity\/schedules$/);
  await expect(page.getByRole('link', { name: 'Monday pull request summary', exact: true })).toBeVisible();
  // Its name is taken now, so it isn't offered again.
  await expect(page.getByRole('link', { name: /^Monday pull request summary Mondays/ })).toHaveCount(0);
});

test('turning on a check-in with an empty list opens it, with lines to start from', async ({ page }) => {
  await open(page, '/activity/schedules?mock=checkin-empty', 'Weekly PR digest');
  await page.getByRole('switch', { name: 'Check-in' }).click();
  await expect(page).toHaveURL(/\/activity\/schedules\/checkin\?on=1$/);
  await page.getByRole('button', { name: 'Use suggested lines' }).click();
  await expect(page.getByLabel('What to check')).toHaveValue(/^Tell me if a deploy to production failed\./);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/\/activity\/schedules$/);
  await expect(page.getByRole('switch', { name: 'Check-in' })).toHaveAttribute('aria-checked', 'true');
});

test('Run now offers the chat the run writes in', async ({ page }) => {
  await open(page, '/activity/schedules', 'Weekly PR digest');
  await page.getByRole('button', { name: 'More for Daily standup summary' }).click();
  await page.getByRole('menuitem', { name: 'Run now' }).click();
  await page.getByRole('button', { name: 'Open chat' }).click();
  await expect(page).toHaveURL(/\/chat\/ses_[0-9a-z]{26}$/);
  await expect(page.getByRole('main').getByText("Summarise yesterday's merged pull requests and open issues in acme/web")).toBeVisible();
});

test('controls are 44 px on a touch phone', async ({ browser }, info) => {
  test.setTimeout(120_000);
  const page = await openPage(browser, info, viewports.phone, 'light');
  try {
    for (const [r, ready] of routes) {
      await open(page, r, ready);
      // The v0.1 shell's top bar is C1's until it lands; only this chunk's main area is judged.
      await page.evaluate(() => {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync('div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; }');
        document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
      });
      expect.soft(await probe(page, { targets: true, overflow: true, leftEdge: false, minText: true, blur: false, shadows: false, nesting: false }), r).toEqual([]);
    }
  } finally {
    await page.context().close();
  }
});
