// C9 Settings: Security and Advanced (docs/design/ui.md §11.6, §11.7, §18.3).
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/security.spec.ts
//   open e2e/shots/out/security/index.html
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

// Until C1 lands, pages render inside the v0.1 shell (a 240 px sidebar, a
// blurred top bar, a main area with its own scroll). This puts them in the
// v0.2 frame (document scroll, content after a 256 px sidebar) so captures and
// probes judge these pages, not the old shell. It does nothing once the v0.1
// top bar is gone. A constructed sheet, not a <style>, so the CSP allows it.
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
  await page.locator('h1').first().waitFor();
}

const settle = (page: Page) => page.waitForTimeout(350);

/** Opens the page, puts it in the v0.2 frame, then runs `then`. */
const framed =
  (then?: (page: Page) => Promise<unknown>) =>
  async (page: Page): Promise<void> => {
    await frame(page);
    if (then) {
      await then(page);
      await settle(page);
    }
  };

const click = (name: string | RegExp, exact = false) =>
  framed(async (page) => {
    await page.getByRole('button', { name, exact }).first().click();
  });

const scrollTo = (text: string | RegExp) =>
  framed(async (page) => {
    await page.getByText(text).first().scrollIntoViewIfNeeded();
  });

const states: ShotState[] = [
  // Security
  { name: 'security', route: '/settings/security', ready: 'Needs attention', setup: framed() },
  { name: 'security-no-findings', route: '/settings/security', mock: 'no-findings', ready: 'When should Bothy ask?', setup: framed() },
  {
    name: 'security-preset-saved',
    route: '/settings/security',
    ready: 'When should Bothy ask?',
    full: false,
    setup: framed(async (page) => {
      await page.getByRole('radio', { name: 'Careful' }).check();
      await page.getByText('Saved', { exact: true }).first().waitFor();
      await page.getByRole('radio', { name: 'Careful' }).scrollIntoViewIfNeeded();
    }),
  },
  {
    name: 'security-timeouts',
    route: '/settings/security',
    ready: 'When should Bothy ask?',
    full: false,
    setup: framed(async (page) => {
      await page.getByLabel('If nobody answers a chat request').selectOption('deny-1h');
      await page.getByText('Saved', { exact: true }).first().waitFor();
      await page.getByText('If nobody answers a scheduled request').scrollIntoViewIfNeeded();
    }),
  },
  { name: 'security-code-on', route: '/settings/security', mock: 'no-findings', ready: 'Running code', full: false, setup: scrollTo('On. Code runs in an isolated workspace.') },
  {
    name: 'security-code-off',
    route: '/settings/security',
    mock: 'sandbox-off',
    ready: 'Running code is off on this server.',
    setup: framed(async (page) => {
      await page.getByRole('button', { name: 'How to turn it on' }).last().click();
      await page.locator('#main').getByRole('button', { name: 'Details', exact: true }).last().click();
      await page.getByRole('button', { name: 'Technical details' }).click();
      await page.getByRole('button', { name: 'How to turn it on' }).last().scrollIntoViewIfNeeded();
    }),
  },
  {
    name: 'security-how-to-fix',
    route: '/settings/security',
    ready: 'Needs attention',
    full: false,
    setup: framed(async (page) => {
      await page.getByRole('button', { name: 'How to fix' }).first().click();
    }),
  },
  { name: 'pause-dialog', route: '/settings/security', ready: 'Pause Bothy', full: false, setup: click('Pause Bothy…') },
  {
    name: 'paused',
    route: '/settings/security',
    mock: 'paused',
    ready: 'Nothing new starts until someone resumes it.',
    full: false,
    setup: scrollTo('Bothy is paused. Nothing new starts until someone resumes it.'),
  },
  { name: 'security-sealed', route: '/settings/security', mock: 'sealed', ready: 'Locked', setup: framed() },
  { name: 'security-member', route: '/settings/security', mock: 'member', ready: 'Only admins can see this page.', setup: framed() },

  // Allowed without asking
  { name: 'permissions', route: '/settings/security/permissions', ready: 'Remove', setup: framed() },
  { name: 'permissions-empty', route: '/settings/security/permissions', mock: 'security-empty', ready: 'Nothing is allowed without asking yet.', setup: framed() },

  // Websites Bothy can reach
  { name: 'websites', route: '/settings/security/websites', ready: 'Blocked recently', setup: framed() },
  {
    name: 'websites-add',
    route: '/settings/security/websites',
    ready: 'Blocked recently',
    full: false,
    setup: framed(async (page) => {
      await page.getByRole('button', { name: 'Add website' }).click();
      await page.getByLabel('Website address').fill('https://Docs.Acme.com/handbook');
    }),
  },
  { name: 'websites-allow', route: '/settings/security/websites', ready: 'Blocked recently', full: false, setup: click('Allow this site…') },

  // Saved keys
  { name: 'keys', route: '/settings/security/keys', ready: 'Anthropic API key', setup: framed() },
  {
    name: 'keys-add',
    route: '/settings/security/keys',
    ready: 'Anthropic API key',
    full: false,
    setup: framed(async (page) => {
      await page.getByRole('button', { name: 'Add key' }).click();
      await page.getByLabel('Name', { exact: true }).fill('Linear API key');
    }),
  },
  {
    name: 'keys-add-errors',
    route: '/settings/security/keys',
    ready: 'Anthropic API key',
    full: false,
    setup: framed(async (page) => {
      await page.getByRole('button', { name: 'Add key' }).click();
      await page.getByRole('button', { name: 'Save key' }).click();
    }),
  },
  { name: 'keys-replace', route: '/settings/security/keys', ready: 'Anthropic API key', full: false, setup: click(/^Replace Anthropic API key/) },
  {
    name: 'keys-delete',
    route: '/settings/security/keys',
    ready: 'Anthropic API key',
    full: false,
    setup: framed(async (page) => {
      await page.getByRole('button', { name: 'More for Anthropic API key' }).click();
      await page.getByRole('menuitem', { name: 'Delete…' }).click();
    }),
  },
  { name: 'keys-sealed', route: '/settings/security/keys', mock: 'sealed', ready: 'Saved keys are locked.', setup: framed() },
  {
    name: 'keys-unlock',
    route: '/settings/security/keys',
    mock: 'sealed',
    ready: 'Saved keys are locked.',
    full: false,
    setup: framed(async (page) => {
      // The sealed banner has an Unlock of its own; this is the page's.
      await page.getByRole('button', { name: 'Unlock', exact: true }).and(page.locator(':not([role="alert"] *)')).click();
      await page.getByLabel('Passphrase').fill('wrong');
      await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click();
      await page.getByText("That passphrase didn't work.").waitFor();
    }),
  },

  // Advanced
  { name: 'advanced', route: '/settings/advanced', ready: 'Last checked', full: true, setup: framed() },
  { name: 'advanced-no-problems', route: '/settings/advanced', mock: 'no-findings', ready: 'No problems found.', full: false, setup: framed() },
  { name: 'advanced-changed', route: '/settings/advanced', ready: 'Last checked', full: false, setup: scrollTo('Memory per workspace (MB)') },
  { name: 'advanced-env', route: '/settings/advanced', ready: 'Last checked', full: false, setup: scrollTo('Set by the server environment') },
  { name: 'advanced-rules', route: '/settings/advanced', ready: 'Last checked', full: false, setup: scrollTo('The first rule that matches decides.') },
  { name: 'advanced-rule-dialog', route: '/settings/advanced', ready: 'Last checked', full: false, setup: click('Edit rule wiki') },
  {
    name: 'advanced-test-rule',
    route: '/settings/advanced',
    ready: 'Last checked',
    full: false,
    setup: framed(async (page) => {
      await page.getByRole('button', { name: 'Test a rule…' }).click();
      await page.getByRole('dialog').getByRole('button', { name: 'Check' }).click();
      await page.getByText('Bothy would ask').waitFor();
    }),
  },
  { name: 'advanced-prices', route: '/settings/advanced', ready: 'Last checked', full: false, setup: click(/^Edit prices for/) },
  { name: 'advanced-config-file', route: '/settings/advanced', ready: 'Last checked', full: false, setup: scrollTo('Download bothy.ui.yaml') },
  { name: 'advanced-safety-rules', route: '/settings/advanced', ready: 'Last checked', full: false, setup: scrollTo("These apply to every chat and can't be changed.") },
];

shots('security', states);
