// C7 Settings: Account, General, Models, People (docs/design/ui.md §11.1–11.5,
// §18.3), every state at 1280 × 800 and 360 × 780 in light and dark.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/settings.spec.ts
//   open e2e/shots/out/settings/index.html
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

// Until C1 lands these pages render inside the v0.1 shell (rail, blurred top
// bar, a main area with its own scroll). This puts them in the v0.2 frame, a
// 256 px sidebar and document scroll, so the captures and probes judge the
// settings pages rather than the old shell (as F2's gallery spec does).
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

// Served from the page's own origin, so the gateway's CSP (style-src 'self') lets it in.
async function frame(page: Page) {
  await page.route('**/__v02-frame.css', (r) => r.fulfill({ contentType: 'text/css', body: V02_FRAME }));
  await page.addStyleTag({ url: '/__v02-frame.css' });
  await page.locator('h1').first().waitFor();
}

const settle = (page: Page) => page.waitForTimeout(350);

const button = (name: string | RegExp) => async (page: Page) => {
  await frame(page);
  await page.getByRole('button', { name, exact: typeof name === 'string' }).first().click();
  await settle(page);
};

/** Reloads with /api/auth/me reporting another way of reaching Bothy (dto.Access). */
const reachedAs = (access: { peer: string; tls: boolean }, then?: (page: Page) => Promise<unknown>) => async (page: Page) => {
  await page.route('**/api/auth/me', async (route) => {
    const r = await route.fetch();
    route.fulfill({ response: r, json: { ...(await r.json()), access } });
  });
  await page.reload();
  await page.waitForLoadState('networkidle');
  await frame(page);
  await page.getByText('Workspace').waitFor();
  await then?.(page);
};

// The personality is a textarea, so its words are a value, not text on the page.
const editor = (page: Page) => page.getByLabel('Personality').waitFor();

async function personMenu(page: Page, name: string) {
  await frame(page);
  await page.getByRole('button', { name: `Options for ${name}` }).click();
  await settle(page);
}

const states: ShotState[] = [
  // Account
  { name: 'account', route: '/settings/account', ready: 'Linked chat apps', setup: frame },
  {
    name: 'account-unlinked',
    route: '/settings/account',
    ready: 'Linked chat apps',
    setup: async (page) => {
      await button('Unlink Telegram')(page);
      await page.getByText('Not linked').waitFor();
    },
  },
  {
    name: 'account-link',
    route: '/settings/account',
    ready: 'Linked chat apps',
    full: false,
    setup: async (page) => {
      // The fixture gateway announces a link to every open page 6 s after a code
      // is made, which would land in a later capture; answering here keeps each one alone.
      await page.route('**/api/pairing', (r) =>
        r.fulfill({ json: { code: '7KQ2MX4D', expires_at: new Date(Date.now() + 15 * 60_000).toISOString(), deep_links: { telegram: 'https://t.me/sams_bothy_bot?start=7KQ2MX4D' } } }),
      );
      await button('Unlink Telegram')(page);
      // From the keyboard: on a phone the "Telegram unlinked." toast sits over the
      // button, and a pointer resting on it holds the toast open.
      await page.getByRole('button', { name: 'Link Telegram' }).focus();
      await page.keyboard.press('Enter');
      await page.getByRole('dialog').getByRole('button', { name: /Link your account/ }).click();
      await page.getByRole('dialog').getByText(/pair/).first().waitFor();
      await settle(page);
    },
  },
  {
    name: 'account-password',
    route: '/settings/account',
    ready: 'Linked chat apps',
    full: false,
    setup: async (page) => {
      await button('Change password…')(page);
      const d = page.getByRole('dialog');
      await d.getByLabel('Current password').fill('correct horse');
      await d.getByLabel('New password').fill('battery st');
      await page.waitForTimeout(700);
    },
  },
  {
    name: 'account-password-errors',
    route: '/settings/account',
    ready: 'Linked chat apps',
    full: false,
    setup: async (page) => {
      await button('Change password…')(page);
      await page.getByRole('dialog').getByRole('button', { name: 'Save password' }).click();
      await settle(page);
    },
  },
  {
    name: 'account-token-create',
    route: '/settings/account',
    ready: 'Linked chat apps',
    full: false,
    setup: async (page) => {
      await button('Create token')(page);
      await page.getByRole('dialog').getByLabel('Name').fill('Open WebUI on the laptop');
      await page.getByRole('dialog').getByLabel('Read your chats and activity').check();
    },
  },
  {
    name: 'account-token-created',
    route: '/settings/account',
    ready: 'Linked chat apps',
    full: false,
    setup: async (page) => {
      await button('Create token')(page);
      const d = page.getByRole('dialog');
      await d.getByLabel('Name').fill('Open WebUI on the laptop');
      await d.getByRole('button', { name: 'Create token' }).click();
      await d.getByText("It won't be shown again.").waitFor();
      await d.getByRole('button', { name: 'Details' }).click();
      await settle(page);
    },
  },
  { name: 'account-revoke', route: '/settings/account', ready: 'Linked chat apps', full: false, setup: button('Revoke Open WebUI…') },
  { name: 'account-member', route: '/settings/account', mock: 'member', ready: 'Linked chat apps', setup: frame },

  // General
  { name: 'general', route: '/settings/general', ready: 'Workspace', setup: frame },
  { name: 'general-https', route: '/settings/general', ready: 'Workspace', setup: reachedAs({ peer: 'public', tls: true }) },
  {
    name: 'general-plain-http',
    route: '/settings/general',
    ready: 'Workspace',
    setup: reachedAs({ peer: 'lan', tls: false }, async (page) => {
      await page.getByRole('button', { name: 'Details' }).click();
      await settle(page);
    }),
  },
  {
    name: 'general-saved-and-error',
    route: '/settings/general',
    ready: 'Workspace',
    full: false,
    setup: async (page) => {
      await frame(page);
      const limit = page.getByLabel('Daily limit');
      await limit.fill('7.5');
      await limit.press('Enter');
      await page.getByText('Saved').first().waitFor();
      const web = page.getByLabel('Web address');
      await web.fill('bothy.acme');
      await web.press('Enter');
      await page.getByText('Use a full address').waitFor();
    },
  },
  { name: 'general-member-blocked', route: '/settings/general', mock: 'member', ready: 'Only admins can see this page.', setup: frame },

  // Personality
  { name: 'personality', route: '/settings/general/personality', ready: editor, setup: frame },
  {
    name: 'personality-conflict',
    route: '/settings/general/personality',
    mock: 'persona-changed',
    ready: editor,
    full: false,
    setup: async (page) => {
      await frame(page);
      await page.getByLabel('Personality').fill(`${await page.getByLabel('Personality').inputValue()}\nKeep answers under five sentences.`);
      await page.getByRole('button', { name: 'Save', exact: true }).click();
      await page.getByText('Someone else changed the personality').waitFor();
    },
  },
  {
    name: 'personality-their-version',
    route: '/settings/general/personality',
    mock: 'persona-changed',
    ready: editor,
    full: false,
    setup: async (page) => {
      await frame(page);
      await page.getByLabel('Personality').fill(`${await page.getByLabel('Personality').inputValue()}\nKeep answers under five sentences.`);
      await page.getByRole('button', { name: 'Save', exact: true }).click();
      await page.getByRole('button', { name: 'See their version' }).click();
      await settle(page);
    },
  },
  {
    name: 'personality-discard',
    route: '/settings/general/personality',
    ready: editor,
    full: false,
    setup: async (page) => {
      await frame(page);
      await page.getByLabel('Personality').fill('Short and sweet.');
      await page.getByRole('button', { name: 'Cancel', exact: true }).click();
      await settle(page);
    },
  },

  // Models
  { name: 'models', route: '/settings/models', mock: 'provider-bedrock', ready: 'Amazon Bedrock', setup: frame },
  {
    name: 'models-backups',
    route: '/settings/models',
    mock: 'provider-bedrock',
    ready: 'Amazon Bedrock',
    setup: async (page) => {
      await button('Backup models')(page);
    },
  },
  {
    name: 'models-change',
    route: '/settings/models',
    mock: 'provider-bedrock',
    ready: 'Amazon Bedrock',
    full: false,
    setup: async (page) => {
      await frame(page);
      await page.getByRole('button', { name: 'Change main model' }).click();
      await settle(page);
    },
  },
  {
    name: 'models-add',
    route: '/settings/models',
    ready: 'Providers',
    full: false,
    setup: async (page) => {
      await button('Add a provider')(page);
      await page.getByRole('dialog').getByText('More providers').click();
      await page.getByRole('dialog').getByRole('radio', { name: /Groq/ }).check();
      await settle(page);
    },
  },
  {
    name: 'models-manage-bedrock',
    route: '/settings/models',
    mock: 'provider-bedrock',
    ready: 'Amazon Bedrock',
    full: false,
    setup: async (page) => {
      await button('Manage Amazon Bedrock')(page);
      await page.getByRole('dialog').getByText(/Connected\./).waitFor();
    },
  },
  {
    name: 'models-manage-key',
    route: '/settings/models',
    ready: 'Providers',
    full: false,
    setup: async (page) => {
      await button('Manage Anthropic')(page);
      const d = page.getByRole('dialog');
      await d.getByText(/Connected\./).waitFor();
      await d.getByRole('button', { name: 'Replace' }).click();
      await d.getByRole('button', { name: 'Details' }).click();
      await settle(page);
    },
  },
  {
    name: 'models-manage-failed',
    route: '/settings/models',
    mock: 'provider-fail:unreachable',
    ready: 'Providers',
    full: false,
    setup: async (page) => {
      await button('Manage Ollama')(page);
      await page.getByRole('dialog').getByText(/isn't answering/).waitFor();
    },
  },
  {
    name: 'models-refine',
    route: '/settings/models',
    ready: 'Refine',
    setup: async (page) => {
      await frame(page);
      await page.getByRole('switch', { name: 'Refine answers by default' }).click();
      await page.getByText('How thorough').waitFor();
      await page.getByText('How thorough').scrollIntoViewIfNeeded();
    },
  },

  // People
  { name: 'people', route: '/settings/people', ready: 'Jordan Kim', setup: frame },
  { name: 'people-menu', route: '/settings/people', ready: 'Jordan Kim', full: false, setup: (page) => personMenu(page, 'Jordan Kim') },
  {
    name: 'people-add',
    route: '/settings/people',
    ready: 'Jordan Kim',
    full: false,
    setup: async (page) => {
      await button('Add person')(page);
      const d = page.getByRole('dialog');
      await d.getByLabel('Name', { exact: true }).fill('Maya Chen');
      await d.getByLabel('Username').fill('maya');
    },
  },
  {
    name: 'people-add-errors',
    route: '/settings/people',
    ready: 'Jordan Kim',
    full: false,
    setup: async (page) => {
      await button('Add person')(page);
      const d = page.getByRole('dialog');
      await d.getByLabel('Username').fill('-alex');
      await d.getByRole('button', { name: 'Create' }).click();
      await settle(page);
    },
  },
  {
    name: 'people-add-taken',
    route: '/settings/people',
    ready: 'Jordan Kim',
    full: false,
    setup: async (page) => {
      await button('Add person')(page);
      const d = page.getByRole('dialog');
      await d.getByLabel('Name', { exact: true }).fill('Alex Morgan');
      await d.getByLabel('Username').fill('alex');
      await d.getByRole('button', { name: 'Create' }).click();
      await d.getByText('That username is taken.').waitFor();
    },
  },
  {
    name: 'people-added',
    route: '/settings/people',
    ready: 'Jordan Kim',
    full: false,
    setup: async (page) => {
      await button('Add person')(page);
      const d = page.getByRole('dialog');
      await d.getByLabel('Name', { exact: true }).fill('Maya Chen');
      await d.getByLabel('Username').fill('maya');
      await d.getByRole('radio', { name: 'Admin' }).click();
      await d.getByRole('button', { name: 'Create' }).click();
      await d.getByText('can sign in now').waitFor();
    },
  },
  {
    name: 'people-limit',
    route: '/settings/people',
    ready: 'Jordan Kim',
    full: false,
    setup: async (page) => {
      await personMenu(page, 'Alex');
      await page.getByRole('menuitem', { name: 'Spending limit…' }).click();
      await page.getByRole('dialog').getByLabel('No personal limit').uncheck();
      await page.getByRole('dialog').getByLabel(/Daily limit for/).fill('3.00');
    },
  },
  {
    name: 'people-reset',
    route: '/settings/people',
    ready: 'Jordan Kim',
    full: false,
    setup: async (page) => {
      await personMenu(page, 'Jordan Kim');
      await page.getByRole('menuitem', { name: 'Reset password…' }).click();
      await page.getByRole('dialog').getByRole('button', { name: 'Reset password' }).click();
      await page.getByRole('dialog').getByText('Send Jordan Kim these details').waitFor();
    },
  },
  {
    name: 'people-turn-off',
    route: '/settings/people',
    ready: 'Jordan Kim',
    full: false,
    setup: async (page) => {
      await personMenu(page, 'Priya Rao');
      await page.getByRole('menuitem', { name: 'Turn off account…' }).click();
      await settle(page);
    },
  },
  { name: 'people-solo', route: '/settings/people', mock: 'people-solo', ready: "It's just you so far.", setup: frame },
];

shots('settings', states);
