// C3 Chat (docs/design/ui.md §7, §18.3): every chat state at 1280 × 800 and
// 375 × 812, light and dark, with the G4 probes on each capture.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/chat.spec.ts
//   open e2e/shots/out/chat/index.html
import { test, type Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

// The fixture chats of web/mock/domains/chat.ts.
const sid = (name: string) => `ses_0chat${name}`.padEnd(30, '0');
const ids = {
  login: sid('login'),
  staging: sid('staging'),
  pricing: sid('pricing'),
  notices: sid('notices'),
  decided: sid('decided'),
  paused: sid('paused'),
  pricingCopy: sid('pricingcopy'),
  checkin: sid('checkin'),
  checkin2: sid('checkin2'),
  brief: 'ses_01jbrief00000000000000000',
};

// Until C1 lands, the chat renders inside the v0.1 shell. This puts it in the
// v0.2 frame (document scroll, content edge after a 256 px sidebar), so the
// captures judge the chat, not the old shell.
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

// A constructed stylesheet, because the build's CSP refuses <style> tags.
const frame = async (page: Page) => {
  await page.evaluate((css) => {
    // Only around the v0.1 shell; C1's shell is part of what the captures judge.
    if (!document.querySelector('div:has(> #main) > header')) return;
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }, V02_FRAME);
  // The old shell's scroll box is gone now, so the chat measures its scroll position again.
  await page.evaluate(() => {
    window.scrollTo(0, document.scrollingElement?.scrollHeight ?? 0);
    window.dispatchEvent(new Event('scroll'));
  });
};

const then =
  (...steps: ((page: Page) => Promise<unknown>)[]) =>
  async (page: Page) => {
    await frame(page);
    for (const s of steps) await s(page);
    await page.waitForTimeout(250);
  };

const click = (name: string | RegExp, role: 'button' | 'menuitem' | 'menuitemradio' = 'button') => (page: Page) => page.getByRole(role, { name }).first().click();
const send = (text: string) => async (page: Page) => {
  await page.getByRole('textbox', { name: 'Message' }).fill(text);
  await page.getByRole('button', { name: 'Send' }).click();
};
const waitText = (text: string) => (page: Page) => page.getByText(text).filter({ visible: true }).first().waitFor();
const menu = (item: string | RegExp) => async (page: Page) => {
  await click('Chat options')(page);
  await click(item, 'menuitem')(page);
};

const chat = (id: string) => `/chat/${id}`;
// The v0.1 approval card has bordered chips and a shadow; C4 replaces it (§8).
const v01Card = { probes: { nesting: false, shadows: false } };

const states: ShotState[] = [
  { name: 'new-github', route: '/chat', ready: 'Summarise the open pull requests in acme/web', setup: frame },
  { name: 'new-admin-no-github', route: '/chat', mock: 'knowledge-empty,github-none', ready: 'Connect GitHub so Bothy can read your code', setup: frame },
  { name: 'new-member', route: '/chat', mock: 'member,knowledge-empty,github-none', ready: 'Turn meeting notes into a list of tasks', setup: frame },
  { name: 'new-starters-failed', route: '/chat', mock: 'starters-fail', ready: 'What can you do for our team?', setup: frame },
  // The welcome chat replaces the empty chat once per account (§5.7).
  { name: 'welcome', route: '/chat', mock: 'welcome', ready: 'Ask me anything to get started.', setup: frame },
  // Where a chat came from (§7.1), what a schedule sent (§7.3) and a check-in's all-clear.
  { name: 'from-chat', route: chat(ids.pricingCopy), ready: 'Pick the plan that fits your team', setup: frame },
  { name: 'from-schedule', route: chat(ids.brief), ready: 'No alerts overnight.', setup: frame },
  { name: 'from-checkin', route: chat(ids.checkin), ready: 'has waited 3 days for a review', setup: frame },
  { name: 'checkin-all-clear', route: chat(ids.checkin2), ready: 'Nothing needs your attention right now.', setup: frame },
  { name: 'populated', route: chat(ids.login), ready: 'Fixed. The sign-in callback', setup: frame },
  { name: 'summary-open', route: chat(ids.login), ready: 'Fixed. The sign-in callback', setup: then(click(/^Got a copy of acme\/web and read 3 files/)) },
  {
    name: 'details-open',
    route: chat(ids.login),
    ready: 'Fixed. The sign-in callback',
    setup: then(click(/^Got a copy of acme\/web and read 3 files/), click(/^Read .*src\/auth\/session\.ts/), (p) => p.getByText('What Bothy asked for').scrollIntoViewIfNeeded()),
  },
  { name: 'attachment-and-failed-step', route: chat(ids.pricing), ready: "Couldn't open status.rival-two.example", setup: frame },
  { name: 'waiting-approval', route: chat(ids.staging), ready: 'Waiting for your approval', setup: frame, ...v01Card },
  { name: 'decided-lines', route: chat(ids.decided), ready: 'Expired: Bothy didn', setup: frame },
  { name: 'decided-show', route: chat(ids.login), ready: 'You approved it', setup: then(click('Show'), (p) => p.getByRole('article').first().scrollIntoViewIfNeeded()), ...v01Card },
  { name: 'working', route: chat(ids.paused), mock: 'chat-hold:working', ready: 'Can you find three venues', setup: then(send('Find three venues near Leeds'), waitText('Working…')) },
  { name: 'streaming', route: chat(ids.paused), mock: 'chat-hold:streaming', ready: 'Can you find three venues', setup: then(send('Find three venues near Leeds'), waitText('Let me check a couple of sources.')) },
  { name: 'tool-running', route: chat(ids.paused), mock: 'chat-hold:tool', ready: 'Can you find three venues', setup: then(send('Find three venues near Leeds'), waitText('Searching the web')) },
  { name: 'notice-provider-error', route: chat(ids.notices), ready: 'had a problem answering', setup: frame },
  { name: 'notice-all-models', route: chat(ids.notices), mock: 'chat-outcome:all', ready: 'No AI model could answer right now.', setup: frame },
  { name: 'notice-auth-admin', route: chat(ids.notices), mock: 'chat-outcome:auth', ready: "stopped accepting Bothy's key", setup: frame },
  { name: 'notice-auth-member', route: chat(ids.notices), mock: 'member,chat-outcome:auth', ready: "Bothy can't reach its AI model right now.", setup: frame },
  { name: 'notice-steps', route: chat(ids.notices), mock: 'chat-outcome:steps', ready: 'Bothy paused after 25 steps', setup: frame },
  { name: 'notice-run-cost', route: chat(ids.notices), mock: 'chat-outcome:run-cost', ready: 'this task reached its', setup: frame },
  { name: 'notice-daily', route: chat(ids.notices), mock: 'chat-outcome:daily', ready: "today's", setup: frame },
  { name: 'notice-loop', route: chat(ids.notices), mock: 'chat-outcome:loop', ready: 'kept repeating the same step', setup: frame },
  { name: 'notice-stopped', route: chat(ids.notices), mock: 'chat-outcome:stopped', ready: 'You stopped Bothy.', setup: frame },
  { name: 'notice-code-off', route: chat(ids.notices), mock: 'sandbox-off,chat-outcome:stopped', ready: "Bothy can't run code on this server", setup: frame },
  { name: 'notice-exposed-why', route: chat(ids.notices), ready: 'From now on, Bothy will ask', setup: then(click('Why?')) },
  { name: 'notice-paused', route: chat(ids.paused), mock: 'paused', ready: "Your message will be answered when it's resumed.", setup: frame },
  { name: 'notice-queued', route: chat(ids.paused), mock: 'chat-queued', ready: 'Can you find three venues', setup: then(send('Also check Manchester'), waitText('Bothy will read this when it finishes the current step.')) },
  { name: 'refine-estimate', route: chat(ids.login), mock: 'refine:estimate', ready: 'Fixed. The sign-in callback', setup: then(click('Refine'), waitText('About $0.20 for this reply.')) },
  { name: 'refine-multiplier', route: chat(ids.login), ready: 'Fixed. The sign-in callback', setup: then(click('Refine'), waitText('Costs about 12× a normal reply.')) },
  { name: 'refine-generic', route: chat(ids.login), mock: 'refine:none', ready: 'Fixed. The sign-in callback', setup: then(click('Refine'), waitText('Slower, and costs several times more.')) },
  // The open menu is a top-layer popover, but the nesting probe still counts it as inside the composer's border.
  { name: 'model-picker', route: chat(ids.login), ready: 'Fixed. The sign-in callback', setup: then(click(/^Model:/)), probes: { nesting: false } },
  {
    name: 'slash-menu',
    route: chat(ids.login),
    ready: 'Fixed. The sign-in callback',
    setup: then(async (p) => {
      await p.getByRole('textbox', { name: 'Message' }).fill('/');
    }),
  },
  {
    name: 'drag-overlay',
    route: chat(ids.login),
    ready: 'Fixed. The sign-in callback',
    setup: then((p) =>
      p.evaluate(() => {
        const dt = new DataTransfer();
        dt.items.add(new File(['x'], 'notes.md'));
        document.querySelector('[aria-label="Messages"]')?.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }));
      }),
    ),
  },
  {
    name: 'upload-error',
    route: chat(ids.login),
    mock: 'upload-fail:large',
    ready: 'Fixed. The sign-in callback',
    setup: then(async (p) => {
      await p.locator('input[type=file]').setInputFiles({ name: 'q3-board-deck.pdf', mimeType: 'application/pdf', buffer: Buffer.from('x') });
      await p.getByText("Couldn't attach q3-board-deck.pdf").waitFor();
    }),
  },
  { name: 'paused-composer', route: chat(ids.login), mock: 'paused', ready: 'Fixed. The sign-in callback', setup: frame },
  { name: 'chat-menu', route: chat(ids.login), ready: 'Fixed. The sign-in callback', setup: then(click('Chat options')) },
  // Through the menu: below 1024 px the title lives in the shell's top bar, which doesn't rename.
  { name: 'rename', route: chat(ids.login), ready: 'Fixed. The sign-in callback', setup: then(menu('Rename')) },
  {
    name: 'chat-details',
    route: chat(ids.login),
    ready: 'Fixed. The sign-in callback',
    setup: then(menu('Chat details'), (p) => p.getByRole('dialog').getByRole('button', { name: 'Technical details' }).click()),
  },
  { name: 'pinned', route: chat(ids.notices), ready: 'had a problem answering', setup: then(menu(/^Pinned/)) },
  { name: 'pinned-adding', route: chat(ids.notices), ready: 'had a problem answering', setup: then(menu(/^Pinned/), click('Pin a file')) },
  // The composer at rest takes focus by itself; the keyboard adds its halo (§7.5).
  { name: 'composer-keyboard-focus', route: chat(ids.login), ready: 'Fixed. The sign-in callback', only: { viewports: ['desktop'] }, setup: then((p) => p.getByRole('button', { name: 'Attach files' }).focus(), (p) => p.keyboard.press('Shift+Tab')) },
  // Bothy's picture didn't load: its logo stands in on every turn (§7.1).
  {
    name: 'picture-fallback',
    route: chat(ids.login),
    ready: 'Fixed. The sign-in callback',
    setup: async (page) => {
      await page.route('**/api/agent/picture*', (r) => r.abort());
      await page.reload();
      await page.getByText('Fixed. The sign-in callback').first().waitFor();
      await then()(page);
    },
  },
  { name: 'missing', route: '/chat/ses_missing', ready: "This chat doesn't exist or was deleted.", setup: frame },
  {
    name: 'load-failed',
    route: chat(ids.login),
    ready: 'Fixed. The sign-in callback',
    setup: async (page) => {
      await page.route('**/api/sessions/*/messages*', (r) => r.fulfill({ status: 503, body: '{}' }));
      await page.reload();
      await page.getByText("This chat didn't load").first().waitFor();
      await frame(page);
    },
  },
  { name: 'dialog-delete', route: chat(ids.login), ready: 'Fixed. The sign-in callback', setup: then(menu('Delete chat…')) },
];

shots('chat', states);

// Opening a chat can add its waiting card to the approvals list; later specs expect the default fixtures.
test.afterAll(async ({ browser }, info) => {
  const page = await browser.newPage({ baseURL: process.env.SHOTS_BASE_URL ?? (info.project.use.baseURL as string | undefined) });
  await page.goto('/chat?mock=');
  await page.close();
});
