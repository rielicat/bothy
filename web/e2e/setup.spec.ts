// C2 setup and sign-in against the production build (docs/design/ui.md §5, §18.3 C2).
import { expect, test, type Page, type Request } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { assertPlainCopy } from './lib/copy';

const axePath = fileURLToPath(new URL('../node_modules/axe-core/axe.min.js', import.meta.url));

if (process.env.E2E_BASE_URL) test.use({ baseURL: process.env.E2E_BASE_URL });

const LINK = (mock: string) => `/setup?mock=${mock}#t=demo-setup-token`;
const writes = (r: Request) => r.method() !== 'GET' && /\/api\/(config|vault|providers\/connect)/.test(new URL(r.url()).pathname);

// The spec files after this one share the mock gateway and expect its default
// fixtures, not a half-finished setup.
test.afterAll(async ({ playwright }) => {
  const api = await playwright.request.newContext({ baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:5197' });
  await api.get('/?mock=');
  await api.dispose();
});

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

// Presses Tab until the focused element matches, so every step is reached the way a keyboard user would.
async function tabTo(page: Page, matches: (el: { tag: string; label: string; type: string }) => boolean, limit = 40) {
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press('Tab');
    const el = await page.evaluate(() => {
      const a = document.activeElement as HTMLElement | null;
      const id = a?.id;
      const label = (id && document.querySelector(`label[for="${CSS.escape(id)}"]`)?.textContent) || a?.getAttribute('aria-label') || a?.textContent || '';
      return { tag: a?.tagName.toLowerCase() ?? '', label: label.trim(), type: (a as HTMLInputElement | null)?.type ?? '' };
    });
    if (matches(el)) return;
  }
  throw new Error('tabTo: never reached the element');
}

test('a token link and detected AWS reach the first chat in 3 screens, 3 typed values and 3 presses', async ({ page }) => {
  const problems = watch(page);
  const titles: string[] = [];
  let typed = 0;
  let presses = 0;
  const type = async (label: string, value: string) => {
    await page.getByLabel(label, { exact: true }).fill(value);
    typed++;
  };
  const press = async (name: string) => {
    await page.getByRole('button', { name }).click();
    presses++;
  };
  const screenTitle = async (title: string) => {
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(title);
    titles.push(title);
  };

  const early: string[] = [];
  page.on('request', (r) => writes(r) && early.push(`${r.method()} ${new URL(r.url()).pathname}`));

  await page.goto(LINK('setup,detect-aws'));
  await screenTitle('Create your account');
  await type('Your name', 'Maya Chen');
  await type('Username', 'maya');
  await type('Password', 'Granite-River-42-Lantern');
  await press('Create account');

  await screenTitle('Connect an AI model');
  // Step 1 wrote team mode and the time zone; nothing else is written until Continue.
  await expect.poll(() => early.length).toBe(1);
  await expect(page.getByText(/^Connected\. Bothy will use Claude Sonnet 4\.5\./)).toBeVisible({ timeout: 10_000 });
  expect(early).toEqual(['PATCH /api/config']);
  await press('Continue');

  await screenTitle('Bothy is ready');
  await expect.poll(() => early).toEqual(['PATCH /api/config', 'POST /api/providers/connect']);
  await press('Start chatting');
  await expect(page).toHaveURL(/\/chat$/);

  expect(titles).toHaveLength(3);
  expect(typed).toBe(3);
  expect(presses).toBe(3);
  expect(problems).toEqual([]);
});

test('a pasted key reaches the first chat in 3 screens and 4 typed values', async ({ page }) => {
  const problems = watch(page);
  const titles: string[] = [];
  let typed = 0;
  const type = async (label: string, value: string) => {
    await page.getByLabel(label, { exact: true }).fill(value);
    typed++;
  };
  const screenTitle = async (title: string) => {
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(title);
    titles.push(title);
  };

  await page.goto(LINK('setup,detect-none'));
  await screenTitle('Create your account');
  await type('Your name', 'Maya Chen');
  await type('Username', 'maya');
  await type('Password', 'Granite-River-42-Lantern');
  await page.getByRole('button', { name: 'Create account' }).click();

  await screenTitle('Connect an AI model');
  await page.getByRole('radio', { name: /^Anthropic/ }).check();
  await type('API key', 'sk-ant-api03-0123456789abcdefghij');
  await expect(page.getByText(/^Connected\. Bothy will use Claude Sonnet 4\.6\./)).toBeVisible({ timeout: 10_000 });
  await page.getByRole('button', { name: 'Continue' }).click();

  await screenTitle('Bothy is ready');
  await page.getByRole('button', { name: 'Start chatting' }).click();
  await expect(page).toHaveURL(/\/chat$/);

  expect(titles).toHaveLength(3);
  expect(typed).toBe(4);
  expect(problems).toEqual([]);
});

test('trying several providers writes nothing until Continue', async ({ page }) => {
  const seen: string[] = [];
  page.on('request', (r) => writes(r) && seen.push(`${r.method()} ${new URL(r.url()).pathname}`));
  await page.goto(LINK('setup,detect-none'));
  await page.getByLabel('Your name').fill('Maya Chen');
  await page.getByLabel('Username').fill('maya');
  await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page.getByText('Choose a provider')).toBeVisible();
  const afterStep1 = seen.length;

  for (const name of ['Anthropic', 'OpenAI']) {
    await page.getByRole('radio', { name: new RegExp(`^${name}`) }).check();
    const key = page.getByLabel('API key', { exact: true });
    await key.fill('sk-test-0123456789abcdefghijklmnop');
    await key.press('Enter');
    await expect(page.getByText(/^Connected\./)).toBeVisible({ timeout: 10_000 });
  }
  expect(seen.slice(afterStep1)).toEqual([]);
  // The last step completes setup on its own; let that land before the next test resets the mock.
  const completed = page.waitForResponse((r) => r.url().endsWith('/api/setup/complete'));
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByText('Bothy is ready')).toBeVisible();
  await completed;
  expect(seen.slice(afterStep1)).toEqual(['POST /api/providers/connect']);
});

test('the whole setup works from the keyboard alone', async ({ page }) => {
  await page.goto(LINK('setup,detect-aws'));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Create your account');
  await tabTo(page, (el) => el.label === 'Your name');
  await page.keyboard.type('Maya Chen');
  await tabTo(page, (el) => el.label === 'Username');
  await page.keyboard.type('maya');
  await tabTo(page, (el) => el.label === 'Password' && el.tag === 'input');
  await page.keyboard.type('Granite-River-42-Lantern');
  await page.keyboard.press('Enter');

  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Connect an AI model');
  await expect(page.getByText(/^Connected\./)).toBeVisible({ timeout: 10_000 });
  // The region is a native select: arrow keys change it and the check runs again.
  await tabTo(page, (el) => el.label === 'Region');
  await page.keyboard.press('ArrowDown');
  await expect(page.getByText(/^Connected\. Bothy will use/)).toBeVisible({ timeout: 10_000 });
  await tabTo(page, (el) => el.tag === 'button' && el.label === 'Continue');
  await page.keyboard.press('Enter');

  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Bothy is ready');
  await tabTo(page, (el) => el.label === 'Start chatting');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/chat$/);
});

test('Continue says why it is disabled and moves focus to the choices', async ({ page }) => {
  await page.goto(LINK('setup,detect-none'));
  await page.getByLabel('Your name').fill('Maya Chen');
  await page.getByLabel('Username').fill('maya');
  await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
  await page.getByRole('button', { name: 'Create account' }).click();
  const cont = page.getByRole('button', { name: 'Continue' });
  await expect(cont).toHaveAttribute('aria-disabled', 'true');
  await expect(cont).toHaveAccessibleDescription('Connect a model to continue.');
  // aria-disabled keeps it focusable; Playwright won't click it, so press it from the keyboard.
  await cont.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('radio', { name: /^Anthropic/ })).toBeFocused();
});

test('sign in maps server errors to plain sentences', async ({ page }) => {
  await page.goto('/login?mock=logged-out');
  await page.getByLabel('Username').fill('sam');
  await page.getByLabel('Password', { exact: true }).fill('wrong');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('alert')).toHaveText("That username and password don't match.");
  await assertPlainCopy(page);
});

test('continue with Google lands in the chat under the gateway CSP', async ({ page }) => {
  const problems = watch(page);
  await page.goto('/login?mock=logged-out,signin-google');
  await page.getByRole('button', { name: 'Continue with Google' }).click();
  await page.waitForURL('**/chat');
  await expect(page.getByRole('button', { name: 'Continue with Google' })).toHaveCount(0);
  expect(problems).toEqual([]);
});

test('a provider sign-in that came back refused says why, once', async ({ page }) => {
  await page.goto('/login?signin_error=no_account&provider=google&mock=logged-out,signin-google');
  await expect(page.getByRole('alert')).toHaveText('No Bothy account uses that Google account yet. Sign in with your password and connect it in Account, or ask an admin.');
  await expect(page).toHaveURL(/\/login$/);
  await assertPlainCopy(page);
});

test('provider buttons are 44 px targets on a touch screen', async ({ browser }) => {
  const ctx = await browser.newContext({ baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:5197', viewport: { width: 360, height: 780 }, hasTouch: true, isMobile: true });
  const page = await ctx.newPage();
  await page.goto('/login?mock=logged-out,signin-google,signin-github');
  const buttons = page.getByRole('button', { name: /^Continue with/ });
  await expect(buttons).toHaveCount(2);
  for (const b of await buttons.all()) expect((await b.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  expect(await page.evaluate(() => document.scrollingElement!.scrollWidth)).toBeLessThanOrEqual(360);
  await ctx.close();
});

const screens: [string, string, (page: Page) => Promise<unknown>][] = [
  ['setup code', '/setup?mock=setup', (page) => page.getByRole('button', { name: 'Continue' }).click()],
  ['remote setup', '/setup?mock=setup,remote', (page) => page.getByText('Finish setup on the server').waitFor()],
  ['account', LINK('setup'), (page) => page.getByRole('button', { name: 'Create account' }).click()],
  [
    'model with a failure',
    LINK('setup,detect-none,provider-fail:key_rejected'),
    async (page) => {
      await page.getByLabel('Your name').fill('Maya Chen');
      await page.getByLabel('Username').fill('maya');
      await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
      await page.getByRole('button', { name: 'Create account' }).click();
      await page.getByRole('radio', { name: /^Anthropic/ }).check();
      await page.getByLabel('API key', { exact: true }).fill('sk-test-0123456789abcdefghijklmnop');
      await page.getByLabel('API key', { exact: true }).press('Enter');
      await page.getByText("That key wasn't accepted.").waitFor({ timeout: 10_000 });
    },
  ],
  [
    'ready',
    LINK('setup,detect-aws'),
    async (page) => {
      await page.getByLabel('Your name').fill('Maya Chen');
      await page.getByLabel('Username').fill('maya');
      await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
      await page.getByRole('button', { name: 'Create account' }).click();
      await page.getByText(/^Connected\./).waitFor({ timeout: 10_000 });
      const completed = page.waitForResponse((r) => r.url().endsWith('/api/setup/complete'));
      await page.getByRole('button', { name: 'Continue' }).click();
      await page.getByText('Bothy is ready').waitFor();
      await completed;
    },
  ],
  ['sign in', '/login?mock=logged-out', (page) => page.getByText('Sign in').first().waitFor()],
  ['sign in with providers', '/login?mock=logged-out,signin-google,signin-github', (page) => page.getByRole('button', { name: 'Continue with GitHub' }).waitFor()],
  [
    'sign in refused by a provider',
    '/login?signin_error=not_allowed&provider=google&mock=logged-out,signin-google',
    (page) => page.getByRole('alert').waitFor(),
  ],
  ['new password', '/password?mock=', (page) => page.getByRole('button', { name: 'Save and continue' }).click()],
];

test.describe('axe and plain copy on every setup screen', () => {
  test.use({ bypassCSP: true });
  for (const scheme of ['light', 'dark'] as const) {
    for (const [name, route, setup] of screens) {
      test(`${name} (${scheme})`, async ({ page }) => {
        await page.emulateMedia({ colorScheme: scheme });
        await page.goto(route);
        await page.waitForLoadState('networkidle');
        await setup(page);
        expect(await axe(page)).toEqual([]);
        await assertPlainCopy(page);
      });
    }
  }
});

test('targets are 44 px on a touch screen', async ({ browser }) => {
  const ctx = await browser.newContext({ baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:5197', viewport: { width: 360, height: 780 }, hasTouch: true, isMobile: true });
  const page = await ctx.newPage();
  await page.goto(LINK('setup,detect-none'));
  await page.getByLabel('Your name').fill('Maya Chen');
  await page.getByLabel('Username').fill('maya');
  await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
  await page.getByRole('button', { name: 'Create account' }).click();
  await page.getByText('Choose a provider').waitFor();
  const small = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('button, input:not([type=radio]), select')]
      .filter((el) => el.offsetParent)
      .map((el) => el.getBoundingClientRect())
      .filter((r) => Math.round(r.height) < 44).length,
  );
  expect(small).toBe(0);
  expect(await page.evaluate(() => document.scrollingElement!.scrollWidth)).toBeLessThanOrEqual(360);
  await ctx.close();
});
