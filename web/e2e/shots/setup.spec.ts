// C2 Setup and sign-in: every state of docs/design/ui.md §5 and §5.9.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/setup.spec.ts
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

const LINK = '/setup#t=demo-setup-token';
const KEY = 'sk-ant-api03-0123456789abcdefghij';

async function account(page: Page) {
  await page.getByLabel('Your name').fill('Maya Chen');
  await page.getByLabel('Username').fill('maya');
  await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
  await page.getByRole('button', { name: 'Create account' }).click();
  await page.getByText('Connect an AI model').waitFor();
}

const connected = (page: Page) => page.getByText(/^Connected\./).waitFor({ timeout: 10_000 });

async function provider(page: Page, name: string, more = false) {
  await account(page);
  if (more) await page.getByRole('button', { name: 'More providers' }).click();
  await page.getByRole('radio', { name: new RegExp(`^${name}`) }).check();
}

async function key(page: Page, name = 'Anthropic') {
  await provider(page, name);
  const field = page.getByLabel('API key', { exact: true });
  await field.fill(KEY);
  await field.press('Enter');
}

async function address(page: Page, url: string, name = 'Other server') {
  await provider(page, name, true);
  const field = page.getByLabel('Server address');
  await field.fill(url);
  await field.press('Enter');
}

const failed = (page: Page) => page.locator('.text-danger-text').first().waitFor({ timeout: 10_000 });
const step2 = (name: string, mock: string, setup: (page: Page) => Promise<unknown>): ShotState => ({ name, route: LINK, mock: `setup,${mock}`, ready: 'Create your account', setup });

const keyFailures = ['key_rejected', 'no_model_access', 'out_of_credit', 'rate_limited', 'provider_error', 'timeout', 'unreachable', 'unknown'];
const addressFailures = ['unreachable', 'no_models', 'tls', 'not_a_model_server'];
const awsFailures = ['aws_no_credentials', 'aws_denied', 'aws_no_models'];

const states: ShotState[] = [
  // Setup code (§5.3) and the remote screen (§5.4).
  { name: 'code', route: '/setup', mock: 'setup', ready: 'Set up Bothy' },
  {
    name: 'code-commands-open',
    route: '/setup',
    mock: 'setup,install:docker',
    ready: 'Set up Bothy',
    setup: (page) => page.getByRole('button', { name: 'Not using Docker?' }).click(),
  },
  { name: 'code-error-empty', route: '/setup', mock: 'setup', ready: 'Set up Bothy', setup: (page) => page.getByRole('button', { name: 'Continue' }).click() },
  { name: 'code-error-wrong', route: '/setup#t=expired-code', mock: 'setup', ready: "That code didn't work" },
  {
    name: 'code-error-tries',
    route: '/setup',
    mock: 'setup,claim-limit',
    ready: 'Set up Bothy',
    setup: async (page) => {
      await page.getByLabel('Setup code').fill('demo-setup-token');
      await page.getByRole('button', { name: 'Continue' }).click();
      await page.getByText('Too many tries.').waitFor();
    },
  },
  {
    name: 'code-error-unreachable',
    route: '/setup',
    mock: 'setup',
    ready: 'Set up Bothy',
    setup: async (page) => {
      await page.route('**/api/setup/claim', (r) => r.abort());
      await page.getByLabel('Setup code').fill('demo-setup-token');
      await page.getByRole('button', { name: 'Continue' }).click();
      await page.getByText("Bothy didn't respond.").waitFor();
    },
  },
  {
    name: 'code-timed-out',
    route: LINK,
    mock: 'setup,grant-expired',
    ready: 'Create your account',
    setup: async (page) => {
      await page.getByLabel('Your name').fill('Maya Chen');
      await page.getByLabel('Username').fill('maya');
      await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
      await page.getByRole('button', { name: 'Create account' }).click();
      await page.getByText('Setup timed out.').waitFor();
    },
  },
  { name: 'remote', route: '/setup', mock: 'setup,remote', ready: 'Finish setup on the server' },

  // Step 1 (§5.5).
  { name: 'account', route: LINK, mock: 'setup', ready: 'Create your account' },
  { name: 'account-errors', route: LINK, mock: 'setup', ready: 'Create your account', setup: (page) => page.getByRole('button', { name: 'Create account' }).click() },
  {
    name: 'account-username-error',
    route: LINK,
    mock: 'setup',
    ready: 'Create your account',
    setup: async (page) => {
      await page.getByLabel('Your name').fill('Maya Chen');
      await page.getByLabel('Username').fill('maya chen');
      await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
      await page.getByRole('button', { name: 'Create account' }).click();
    },
  },
  {
    name: 'account-password-short',
    route: LINK,
    mock: 'setup',
    ready: 'Create your account',
    setup: (page) => page.getByLabel('Password', { exact: true }).fill('river-42'),
  },
  {
    name: 'account-password-long-enough',
    route: LINK,
    mock: 'setup',
    ready: 'Create your account',
    setup: async (page) => {
      await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
      await page.getByRole('button', { name: 'Show' }).click();
    },
  },
  {
    name: 'account-password-common',
    route: LINK,
    mock: 'setup',
    ready: 'Create your account',
    setup: async (page) => {
      await page.getByLabel('Your name').fill('Maya Chen');
      await page.getByLabel('Username').fill('maya');
      await page.getByLabel('Password', { exact: true }).fill('password1234');
      await page.getByRole('button', { name: 'Create account' }).click();
      await page.getByText('This password is too common.').waitFor();
    },
  },
  {
    name: 'account-network-error',
    route: LINK,
    mock: 'setup',
    ready: 'Create your account',
    setup: async (page) => {
      await page.route('**/api/setup/owner', (r) => r.abort());
      await page.getByLabel('Your name').fill('Maya Chen');
      await page.getByLabel('Username').fill('maya');
      await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
      await page.getByRole('button', { name: 'Create account' }).click();
      await page.getByText("Bothy didn't respond.").waitFor();
    },
  },

  // Step 2 (§5.6): detection.
  step2('model-detecting', 'detect-slow', async (page) => {
    await account(page);
    await page.getByText('Looking for models on this server…').waitFor();
  }),
  step2('model-nothing-found', 'detect-none', async (page) => {
    await account(page);
    await page.getByText('Choose a provider').waitFor();
  }),
  step2('model-found-ollama-and-key', '', async (page) => {
    await account(page);
    await connected(page);
  }),
  step2('model-continue-reason', 'detect-none', async (page) => {
    await account(page);
    // aria-disabled, so Playwright won't click it; the keyboard still can.
    await page.getByRole('button', { name: 'Continue' }).focus();
    await page.keyboard.press('Enter');
  }),

  // AWS (S3).
  step2('model-aws-checking', 'detect-aws,check-slow', async (page) => {
    await account(page);
    await page.getByText('Checking AWS access…').waitFor();
  }),
  step2('model-aws-connected', 'detect-aws', async (page) => {
    await account(page);
    await connected(page);
  }),
  step2('model-aws-region-changed', 'detect-aws', async (page) => {
    await account(page);
    await connected(page);
    await page.getByLabel('Region').selectOption('eu-west-1');
    await page.getByText('Checking which models you can use…').waitFor();
    await connected(page);
  }),
  ...awsFailures.map((code) =>
    step2(`model-aws-${code.replaceAll('_', '-')}`, `detect-aws,provider-fail:${code}`, async (page) => {
      await account(page);
      await failed(page);
      await page.getByRole('button', { name: 'Details' }).click();
    }),
  ),
  step2('model-bedrock-not-found', 'detect-none', async (page) => {
    await provider(page, 'Amazon Bedrock');
  }),

  // Local servers.
  step2('model-ollama-connected', 'detect-ollama', async (page) => {
    await account(page);
    await connected(page);
  }),
  ...[24000, 12000, 4096].map((n) =>
    step2(`model-ollama-note-${n}`, `detect-ollama,model-context:${n}`, async (page) => {
      await account(page);
      await connected(page);
    }),
  ),
  step2('model-ollama-no-models', 'ollama-empty', async (page) => {
    await account(page);
    await failed(page);
  }),
  step2('model-more-providers', 'detect-none', async (page) => {
    await account(page);
    await page.getByRole('button', { name: 'More providers' }).click();
  }),
  step2('model-other-private-address', 'detect-none', async (page) => {
    await address(page, 'http://127.0.0.1:9/v1');
    await page.getByRole('button', { name: 'Allow 127.0.0.1:9' }).waitFor({ timeout: 10_000 });
  }),
  step2('model-other-allowed', 'detect-none', async (page) => {
    await address(page, 'http://127.0.0.1:9/v1');
    await page.getByRole('button', { name: 'Allow 127.0.0.1:9' }).click();
    await connected(page);
  }),
  step2('model-other-model-name', 'detect-none,no-model-list', async (page) => {
    await address(page, 'https://models.example.com/v1');
    await page.getByLabel('Model name').waitFor({ timeout: 10_000 });
  }),
  ...addressFailures.map((code) =>
    step2(`model-local-${code.replaceAll('_', '-')}`, `detect-none,provider-fail:${code}`, async (page) => {
      await address(page, 'http://host.docker.internal:11434', 'Ollama');
      await failed(page);
      await page.getByRole('button', { name: 'Details' }).click();
    }),
  ),

  // Key providers.
  step2('model-key', 'detect-none', (page) => provider(page, 'Anthropic')),
  step2('model-key-checking', 'detect-none,check-slow', async (page) => {
    await key(page);
    await page.getByText('Checking the key…').waitFor();
  }),
  step2('model-key-connected', 'detect-none', async (page) => {
    await key(page);
    await connected(page);
  }),
  step2('model-key-change', 'detect-none', async (page) => {
    await key(page);
    await connected(page);
    await page.getByRole('button', { name: 'Change' }).click();
  }),
  step2('model-env-key', '', async (page) => {
    await account(page);
    await page.getByRole('radio', { name: /^Use the Anthropic key/ }).check();
    await connected(page);
  }),
  ...keyFailures.map((code) =>
    step2(`model-key-${code.replaceAll('_', '-')}`, `detect-none,provider-fail:${code}`, async (page) => {
      await key(page);
      await failed(page);
    }),
  ),
  step2('model-key-failure-details', 'detect-none,provider-fail:key_rejected', async (page) => {
    await key(page);
    await failed(page);
    await page.getByRole('button', { name: 'Details' }).click();
  }),

  // Continue (§5.6.5).
  step2('model-saving', 'detect-aws', async (page) => {
    await account(page);
    await connected(page);
    await page.route('**/api/providers/connect', () => new Promise(() => {}));
    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByText('Saving…').waitFor();
  }),
  step2('model-save-failed', 'detect-aws', async (page) => {
    await account(page);
    await connected(page);
    await page.route('**/api/providers/connect', (r) =>
      r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":{"code":"internal","message":"write /data/bothy.ui.yaml: read-only file system"}}' }),
    );
    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByText("Bothy couldn't save this connection.").waitFor();
  }),

  // Step 3 (§5.7).
  step2('ready', 'detect-aws', async (page) => {
    await account(page);
    await connected(page);
    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByText('Bothy is ready').waitFor();
  }),

  // Sign in and the first password (§5.9).
  { name: 'sign-in', route: '/login', mock: 'logged-out', ready: 'Sign in' },
  { name: 'sign-in-expired', route: '/login?next=%2Fchat', mock: 'logged-out', ready: 'You were signed out.' },
  { name: 'sign-in-finish-setup', route: '/setup', mock: 'logged-out,setup-owner', ready: 'Sign in to finish setting up Bothy.' },
  { name: 'sign-in-providers', route: '/login', mock: 'logged-out,signin-google,signin-github', ready: 'Continue with GitHub' },
  {
    // Through the mock provider's callback, which leaves the refused account in the refusal cookie.
    name: 'sign-in-provider-refused',
    route: '/login',
    mock: 'logged-out,signin-google,signin-fail',
    ready: 'Continue with Google',
    setup: async (page) => {
      await page.getByRole('button', { name: 'Continue with Google' }).click();
      await page.getByRole('alert').filter({ hasText: "kim@gmail.com isn't on this Bothy's sign-in list." }).waitFor();
    },
  },
  ...(
    [
      ['wrong', 'logged-out', 'wrong'],
      ['locked', 'logged-out,login-locked', 'Granite-River-42-Lantern'],
      ['turned-off', 'logged-out,login-disabled', 'Granite-River-42-Lantern'],
    ] as const
  ).map(
    ([name, mock, password]): ShotState => ({
      name: `sign-in-${name}`,
      route: '/login',
      mock,
      ready: 'Sign in',
      setup: async (page) => {
        await page.getByLabel('Username').fill('sam');
        await page.getByLabel('Password', { exact: true }).fill(password);
        await page.getByRole('button', { name: 'Sign in' }).click();
        await page.getByRole('alert').waitFor();
      },
    }),
  ),
  {
    name: 'sign-in-network',
    route: '/login',
    mock: 'logged-out',
    ready: 'Sign in',
    setup: async (page) => {
      await page.route('**/api/auth/login', (r) => r.abort());
      await page.getByLabel('Username').fill('sam');
      await page.getByLabel('Password', { exact: true }).fill('Granite-River-42-Lantern');
      await page.getByRole('button', { name: 'Sign in' }).click();
      await page.getByRole('alert').waitFor();
    },
  },
  {
    name: 'new-password',
    route: '/login?next=%2Fchat',
    mock: 'logged-out,must-change',
    ready: 'Sign in',
    setup: async (page) => {
      await page.getByLabel('Username').fill('jordan');
      await page.getByLabel('Password', { exact: true }).fill('TempPass-1234-xyz');
      await page.getByRole('button', { name: 'Sign in' }).click();
      await page.getByText('Choose a new password').waitFor();
      await page.getByLabel('New password').fill('river-42');
    },
  },
  {
    name: 'new-password-error',
    route: '/login?next=%2Fchat',
    mock: 'logged-out,must-change',
    ready: 'Sign in',
    setup: async (page) => {
      await page.getByLabel('Username').fill('jordan');
      await page.getByLabel('Password', { exact: true }).fill('TempPass-1234-xyz');
      await page.getByRole('button', { name: 'Sign in' }).click();
      await page.getByText('Choose a new password').waitFor();
      await page.getByLabel('New password').fill('password1234');
      await page.getByRole('button', { name: 'Save and continue' }).click();
      await page.getByText('This password is too common.').waitFor();
    },
  },
  { name: 'new-password-after-reload', route: '/password', ready: 'Choose a new password' },
];

// Setup and sign in sit outside the content column, so the left-edge probe is off.
shots('setup', states, { probes: { leftEdge: false } });
