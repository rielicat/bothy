// C3 Chat end to end (docs/design/ui.md §18.3): the production build under
// the gateway's CSP with the fixture gateway. Each test opens its first page
// with ?mock=, which resets the fixtures, so tests don't see each other's chats.
import { expect, test, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));
const sid = (name: string) => `ses_0chat${name}`.padEnd(30, '0');

function watch(page: Page) {
  const problems: string[] = [];
  page.on('console', (m) => m.type() === 'error' && /Content Security Policy|Refused to/.test(m.text()) && problems.push(m.text()));
  page.on('pageerror', (e) => problems.push(String(e)));
  page.on('request', (r) => !r.url().startsWith('http://localhost:') && !r.url().startsWith('data:') && problems.push(`external request ${r.url()}`));
  return problems;
}

// Later spec files expect the default fixtures (an untouched approvals inbox).
test.afterEach(async ({ page }) => {
  await page.goto('/chat?mock=');
});

async function send(page: Page, text: string) {
  await page.getByRole('textbox', { name: 'Message' }).fill(text);
  await page.getByRole('textbox', { name: 'Message' }).press('Enter');
}

test('a new chat stores nothing before the first message, and a suggestion fills the composer without sending', async ({ page }) => {
  const problems = watch(page);
  const posts: string[] = [];
  // Only chat writes count: the shell's attention dot reads findings with POST /api/security-audit {fix: false}.
  page.on('request', (r) => r.method() === 'POST' && new URL(r.url()).pathname.startsWith('/api/sessions') && posts.push(new URL(r.url()).pathname));
  await page.goto('/chat?mock=');
  await page.getByRole('button', { name: 'What can you do for our team?' }).click();
  await expect(page.getByRole('textbox', { name: 'Message' })).toHaveValue('What can you do for our team?');
  await expect(page.getByRole('textbox', { name: 'Message' })).toBeFocused();
  await page.waitForTimeout(300);
  expect(posts).toEqual([]);

  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/chat\/ses_/);
  await expect(page.getByText('Let me check a couple of sources.').first()).toBeVisible({ timeout: 10_000 });
  expect(posts.slice(0, 2)).toEqual(['/api/sessions', expect.stringMatching(/^\/api\/sessions\/ses_[^/]+\/messages$/)]);
  expect(problems).toEqual([]);
});

test('no Working or Thinking line once the first words arrive', async ({ page }) => {
  const problems = watch(page);
  await page.goto(`/chat/${sid('paused')}?mock=`);
  await send(page, 'Find three venues near Leeds');
  await expect(page.getByText('Let me check a couple of sources.').first()).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText(/Working…|Thinking/)).toHaveCount(0);
  await expect(page.getByText('Here is what I found about').first()).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText(/Working…|Thinking/)).toHaveCount(0);
  expect(problems).toEqual([]);
});

test('a provider failure is said once, without the raw message in the main flow', async ({ page }) => {
  const problems = watch(page);
  await page.goto(`/chat/${sid('paused')}?mock=chat-fail`);
  await send(page, 'Summarise this week');
  await expect(page.getByText('Anthropic had a problem answering.')).toHaveCount(1, { timeout: 10_000 });
  await expect(page.getByRole('alert')).toHaveCount(1);
  expect(await page.evaluate(() => document.body.innerText)).not.toContain('.).');
  await page.getByRole('button', { name: 'Details', exact: true }).last().click();
  await expect(page.getByText(/529 overloaded/)).toBeVisible();
  expect(problems).toEqual([]);
});

test('a decided card stays in the transcript after a reload', async ({ page }) => {
  const problems = watch(page);
  const id = sid('paused');
  await page.goto(`/chat/${id}?mock=`);
  await send(page, 'Please run a script to tidy my reports');
  // The card right below says what it asks, so its waiting line only says whose approval it waits for.
  await expect(page.getByRole('button', { name: /^Waiting for your approval/ })).toBeVisible({ timeout: 10_000 });
  const card = page.getByRole('article').filter({ hasText: 'tidy.py' });
  await card.getByRole('button', { name: /^(Allow once|Run command)/ }).click();
  await expect(page.getByText('The dry run found')).toBeVisible({ timeout: 10_000 });
  // The step's own line sits right above the decided line, which so doesn't name the action again.
  await expect(page.getByText(/^You approved it/)).toBeVisible();
  // Without ?mock= the fixtures stay as they are.
  await page.goto(`/chat/${id}`);
  await expect(page.getByText(/^You approved it/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Ran python3 tidy.py --dry-run reports/' })).toBeVisible();
  expect(problems).toEqual([]);
});

test('Enter adds a line instead of sending on a touch screen', async ({ browser }, info) => {
  const ctx = await browser.newContext({ baseURL: info.project.use.baseURL as string, hasTouch: true, isMobile: true, viewport: { width: 360, height: 780 } });
  const page = await ctx.newPage();
  const posts: string[] = [];
  page.on('request', (r) => r.method() === 'POST' && posts.push(new URL(r.url()).pathname));
  await page.goto(`/chat/${sid('login')}?mock=`);
  const box = page.getByRole('textbox', { name: 'Message' });
  await box.click();
  await page.keyboard.type('first');
  await page.keyboard.press('Enter');
  await page.keyboard.type('second');
  await expect(box).toHaveValue('first\nsecond');
  expect(posts.filter((p) => p.endsWith('/messages'))).toEqual([]);
  await ctx.close();
});

test('a clean chat is listed at once, under the chat it came from and selected', async ({ page }) => {
  const problems = watch(page);
  await page.goto(`/chat/${sid('login')}?mock=`);
  await page.getByRole('heading', { level: 1, name: 'Fix login redirect' }).waitFor();
  await page.getByRole('button', { name: 'Chat options' }).first().click();
  await page.getByRole('menuitem', { name: 'Continue in a clean chat' }).click();
  await page.getByRole('dialog', { name: 'Continue in a clean chat?' }).getByRole('button', { name: 'Continue in a clean chat' }).click();
  await expect(page).not.toHaveURL(new RegExp(sid('login')));
  const nav = page.getByRole('navigation', { name: 'Main' });
  const kids = nav.getByRole('list', { name: 'Related to “Fix login redirect”' });
  await expect(kids.getByRole('link', { name: 'Fix login redirect (clean)' })).toHaveAttribute('aria-current', 'page');
  await expect(nav.getByRole('button', { name: 'Related chats for “Fix login redirect”' })).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByRole('main').getByText('Continued from “Fix login redirect”')).toBeVisible();
  expect(problems).toEqual([]);
});

test('New related chat opens one under this chat without asking', async ({ page }) => {
  await page.goto(`/chat/${sid('pricing')}?mock=`);
  await page.getByRole('heading', { level: 1, name: 'Q3 pricing notes' }).waitFor();
  await page.getByRole('button', { name: 'Chat options' }).first().click();
  await page.getByRole('menuitem', { name: 'New related chat' }).click();
  await expect(page).not.toHaveURL(new RegExp(sid('pricing')));
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const kids = page.getByRole('navigation', { name: 'Main' }).getByRole('list', { name: 'Related to “Q3 pricing notes”' });
  await expect(kids.getByRole('link', { name: 'Q3 pricing notes (clean)' })).toHaveAttribute('aria-current', 'page');
  // The chat continued earlier stays in the family.
  await expect(kids.getByRole('link', { name: 'Pricing page copy for the launch' })).toBeVisible();
});

const screens = [
  '/chat',
  `/chat/${sid('login')}`,
  `/chat/${sid('staging')}`,
  `/chat/${sid('decided')}`,
  `/chat/${sid('notices')}`,
  `/chat/${sid('pricing')}`,
  // A chat continued from another, a schedule's result and a check-in someone answered.
  `/chat/${sid('pricingcopy')}`,
  `/chat/ses_01jbrief00000000000000000`,
  `/chat/${sid('checkin2')}`,
];

test('chat screens use plain words', async ({ page }) => {
  for (const r of screens) {
    await page.goto(`${r}?mock=`);
    await page.waitForLoadState('networkidle');
    await assertPlainCopy(page);
  }
});

test.describe('axe on the chat screens', () => {
  // axe is injected as an inline script, which the CSP would block.
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    test(`including contrast (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      for (const r of screens) {
        await page.goto(`${r}?mock=`);
        await page.waitForLoadState('networkidle');
        await page.addScriptTag({ path: axePath });
        const violations = await page.evaluate(async () => {
          const res = await (window as unknown as { axe: { run: (n: Node) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run(document);
          return res.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
        });
        expect.soft(violations, `${r} (${scheme})`).toEqual([]);
      }
    });
  }

  // A family open around the open chat and a closed one with a toggle, pinned at 1280 and in the drawer at 360.
  for (const [name, size] of [
    ['wide', { width: 1280, height: 820 }],
    ['phone', { width: 360, height: 760 }],
  ] as const) {
    test(`on a sidebar with related chats (${name})`, async ({ page }) => {
      await page.setViewportSize(size);
      await page.goto(`/chat/${sid('pricingcopy')}?mock=`);
      if (name === 'phone') await page.getByRole('button', { name: /^Open menu/ }).click();
      const where = name === 'phone' ? page.getByRole('dialog', { name: 'Menu' }) : page.getByRole('navigation', { name: 'Main' });
      await where.getByRole('list', { name: 'Related to “Q3 pricing notes”' }).waitFor();
      await page.addScriptTag({ path: axePath });
      const violations = await page.evaluate(async () => {
        const res = await (window as unknown as { axe: { run: (n: Node) => Promise<{ violations: { id: string; nodes: { target: string[] }[] }[] }> } }).axe.run(document);
        return res.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
      });
      expect(violations).toEqual([]);
    });
  }
});
