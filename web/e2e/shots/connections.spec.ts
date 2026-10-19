// C8 Settings → Connections (docs/design/ui.md §12, §18.3): every route and
// state, at 1280 × 800 and 360 × 780, light and dark, with the G4 probes.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/connections.spec.ts
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

// Until C1 lands, pages render inside the v0.1 shell (240 px sidebar, blurred
// top bar, a main area with its own scroll). This puts them in the v0.2 frame
// (document scroll, content edge after a 256 px sidebar), as the primitives
// spec does, so the captures judge these pages rather than the old shell.
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

type Step = (page: Page) => Promise<unknown>;

// A constructed stylesheet goes through CSSOM, which the production CSP allows; a <style> tag would be blocked.
const frame: Step = async (page) => {
  await page.evaluate((css) => {
    // Only around the v0.1 shell; C1's shell is part of what the captures judge.
    if (!document.querySelector('div:has(> #main) > header')) return;
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }, V02_FRAME);
  await page.locator('h1').first().waitFor();
};
const steps =
  (...list: Step[]) =>
  async (page: Page) => {
    await frame(page);
    for (const s of list) await s(page);
    // Let disclosures and dialogs finish their entry motion before the capture.
    await page.waitForTimeout(300);
  };
const click = (name: string | RegExp, role: 'button' | 'radio' | 'menuitem' | 'link' = 'button'): Step => (page) => page.getByRole(role, { name }).first().click();
const fill = (label: string | RegExp, value: string, enter = false): Step => async (page) => {
  const field = page.getByLabel(label, { exact: typeof label === 'string' }).first();
  await field.fill(value);
  if (enter) await field.press('Enter');
};
const see = (text: string | RegExp): Step => (page) => page.getByText(text).filter({ visible: true }).first().waitFor();
const menu = (trigger: string | RegExp, item: string | RegExp): Step => async (page) => {
  await page.getByRole('button', { name: trigger }).first().click();
  await page.getByRole('menuitem', { name: item }).click();
};
const dialog = (name: string | RegExp): Step => (page) => page.getByRole('dialog', { name }).waitFor();

const SLACK_APP = 'xapp-' + '1-A0123456789-1234567890123-abcdef';
const SLACK_BOT = 'xoxb-' + '1234567890-1234567890123-AbCdEfGhIjKlMnOp';
const TELEGRAM = '123456789:' + 'AAH4kq9xV1t8mZp2Lq0wE3rTyUiOpAsDfGh';
const DISCORD = 'MTIzNDU2Nzg5MDEyMzQ1Njc4' + '.GhIjKl.MnOpQrStUvWxYzAbCdEfGhIjKlMnOpQrStUvWx';
const PEM = '-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1mock\n-----END RSA PRIVATE KEY-----';

const disconnect = (app: string): Step[] => [
  click(`Disconnect ${app}…`),
  (p) => p.getByRole('dialog', { name: `Disconnect ${app}?` }).getByRole('button', { name: `Disconnect ${app}`, exact: true }).click(),
  see(/Paste the/),
];
const slackTokens: Step[] = [fill('App-level token', SLACK_APP), fill('Bot token', SLACK_BOT, true)];
// The token and existing-app paths sit under "Other ways to connect".
const other: Step = click('Other ways to connect');
const appKey: Step[] = [other, click('A GitHub App you already have', 'radio'), fill('App ID', '123456'), click('Paste instead'), fill('Private key', PEM)];

const states: ShotState[] = [
  // Overview (§12.1)
  { name: 'overview', route: '/settings/connections', setup: steps(see('Web search')) },
  { name: 'overview-every-status', route: '/settings/connections', mock: 'channel-down:telegram,mcp-installing', setup: steps(see('Installing…')) },
  { name: 'overview-off-and-empty', route: '/settings/connections', mock: 'chat-apps-off,github-none,mcp-none', setup: steps(see('No other tools connected.')) },
  { name: 'overview-member', route: '/settings/connections', mock: 'member', setup: steps(see('Only admins can see this page.')) },
  { name: 'add-mcp-web', route: '/settings/connections', setup: steps(click('Add an MCP server'), dialog('Add an MCP server'), fill('Name', 'linear-eu')), full: false },
  {
    name: 'add-mcp-command',
    route: '/settings/connections',
    setup: steps(click('Add an MCP server'), click('A command on this server', 'radio'), fill('Name', 'sentry'), fill('Package', '@sentry/mcp-server'), fill(/^Version/, '1.4.0'), fill(/^Websites it can reach/, 'sentry.io')),
    full: false,
  },
  {
    name: 'add-mcp-command-details',
    route: '/settings/connections',
    setup: steps(click('Add an MCP server'), click('A command on this server', 'radio'), fill('Package', '@sentry/mcp-server'), fill(/^Version/, '1.4.0'), click('Details')),
    full: false,
  },
  { name: 'add-mcp-code-off', route: '/settings/connections', mock: 'sandbox-off', setup: steps(click('Add an MCP server'), click('A command on this server', 'radio'), see('running code is off on this server')), full: false },
  {
    name: 'add-mcp-errors',
    route: '/settings/connections',
    setup: steps(click('Add an MCP server'), fill('Name', 'linear-eu'), click('Add server'), see('Enter the full address, like https://mcp.linear.app/mcp.')),
    full: false,
  },

  // GitHub (§12.2)
  { name: 'github-create-app', route: '/settings/connections/github', mock: 'github-none', setup: steps(fill('Organization', 'acme'), see('Continue on GitHub')) },
  { name: 'github-create-personal', route: '/settings/connections/github', mock: 'github-none', setup: steps(click('Your personal account', 'radio'), click("Change the app's name"), click('What can the app do?')) },
  { name: 'github-app-pending', route: '/settings/connections/github', mock: 'github-app-pending', setup: steps(see('Finish installing Bothy (acme) on GitHub')) },
  { name: 'github-app-requested', route: '/settings/connections/github?github_app=requested', mock: 'github-app-pending', setup: steps(see('needs to approve')) },
  { name: 'github-app-retired', route: '/settings/connections/github', mock: 'github-app-retired', setup: steps(see('Bothy no longer uses Bothy (acme).')) },
  { name: 'github-app-landing-installed', route: '/settings/connections/github?github_app=installed', mock: 'github-app-made', setup: steps(dialog('Add repositories'), see('acme/mobile')), full: false },
  { name: 'github-app-not-finished', route: '/settings/connections/github?github_app=not_finished&apps=acme', mock: 'github-none', setup: steps(see("couldn't finish setting up the app")) },
  { name: 'github-app-manage', route: '/settings/connections/github', mock: 'github-app-made', setup: steps(see('Connected to acme as Bothy (acme)')) },
  { name: 'github-connect', route: '/settings/connections/github', mock: 'github-none', setup: steps(other, see('Personal access token')) },
  { name: 'github-permissions', route: '/settings/connections/github', mock: 'github-none', setup: steps(other, click('Which permissions?')) },
  { name: 'github-token-connected', route: '/settings/connections/github', mock: 'github-none', setup: steps(other, fill('Token', 'github_pat_11ABCDEFG0123456789abcdef', true), see('Bothy can see')) },
  { name: 'github-token-filtered', route: '/settings/connections/github', mock: 'github-none', setup: steps(other, fill('Token', 'github_pat_11ABCDEFG0123456789abcdef', true), see('Bothy can see'), fill('Filter repositories', 'de')) },
  { name: 'github-fail-rejected', route: '/settings/connections/github', mock: 'github-none', setup: steps(other, fill('Token', 'github_pat_bad0123456789abcdefghij', true), see("didn't accept"), click('Details')) },
  { name: 'github-fail-empty', route: '/settings/connections/github', mock: 'github-none', setup: steps(other, fill('Token', 'github_pat_empty0123456789abcdefgh', true), see("can't see any")) },
  { name: 'github-fail-unreachable', route: '/settings/connections/github', mock: 'github-none,github-fail:unreachable', setup: steps(other, fill('Token', 'github_pat_11ABCDEFG0123456789abcdef', true), see("couldn't reach")) },
  { name: 'github-app', route: '/settings/connections/github', mock: 'github-none', setup: steps(other, click('A GitHub App you already have', 'radio')) },
  { name: 'github-app-installed', route: '/settings/connections/github', mock: 'github-none', setup: steps(...appKey, see('Installed on acme')) },
  { name: 'github-app-not-installed', route: '/settings/connections/github', mock: 'github-none,github-fail:not-installed', setup: steps(...appKey, see("isn't installed")) },
  { name: 'github-app-organizations', route: '/settings/connections/github', mock: 'github-none,github-fail:installations', setup: steps(...appKey, (p) => p.getByRole('combobox', { name: 'Organization' }).waitFor()) },
  { name: 'github-enterprise', route: '/settings/connections/github', mock: 'github-none', setup: steps(click('Using GitHub Enterprise?')) },
  {
    name: 'github-saved',
    route: '/settings/connections/github',
    mock: 'github-none',
    setup: steps(other, fill('Token', 'github_pat_11ABCDEFG0123456789abcdef', true), see('Bothy can see'), (p) => p.getByLabel('acme/web').check(), (p) => p.getByLabel('acme/api').check(), click('Save'), see('Bothy is reading')),
  },
  { name: 'github-manage', route: '/settings/connections/github', setup: steps(see('Deploy environments'), see(/Read (today|yesterday)/)) },
  { name: 'github-manage-app', route: '/settings/connections/github', mock: 'github-app', setup: steps(see('GitHub App')) },
  { name: 'github-details', route: '/settings/connections/github', setup: steps(click('Details'), see('Requests left this hour')) },
  { name: 'github-repo-menu', route: '/settings/connections/github', setup: steps(click('Options for acme/web')), full: false },
  { name: 'github-add-repos', route: '/settings/connections/github', setup: steps(click('Add repositories'), dialog('Add repositories'), see('acme/mobile')), full: false },
  { name: 'github-disconnect', route: '/settings/connections/github', setup: steps(click('Disconnect GitHub…'), dialog('Disconnect GitHub?')), full: false },
  { name: 'github-envs-empty', route: '/settings/connections/github', mock: 'no-environments', setup: steps(see('No deploy environments yet.')) },
  { name: 'env-dialog-new', route: '/settings/connections/github', mock: 'no-environments', setup: steps(click('Add environment'), dialog('Add environment'), (p) => p.getByRole('combobox', { name: 'Deploy workflow' }).waitFor()), full: false },
  {
    name: 'env-dialog-two-approvals',
    route: '/settings/connections/github',
    setup: steps(menu('Options for production', 'Edit…'), dialog('Edit production'), see('Two different people must approve')),
    full: false,
  },
  {
    name: 'env-dialog-approvers-open',
    route: '/settings/connections/github',
    mock: 'no-environments',
    setup: steps(click('Add environment'), dialog('Add environment'), (p) => p.getByRole('combobox', { name: 'Who can approve' }).click()),
    full: false,
  },
  {
    name: 'env-dialog-errors',
    route: '/settings/connections/github',
    mock: 'no-environments',
    setup: steps(click('Add environment'), dialog('Add environment'), click(/^Save( environment)?$/), see('Give the environment a name.')),
    full: false,
  },
  {
    name: 'env-dialog-text-workflow',
    route: '/settings/connections/github',
    mock: 'no-environments,no-workflows',
    setup: steps(click('Add environment'), dialog('Add environment'), click('Workflow inputs'), click('Add an input'), (p) => p.getByRole('button', { name: 'Add an input' }).scrollIntoViewIfNeeded()),
    full: false,
  },
  { name: 'env-delete', route: '/settings/connections/github', setup: steps(menu('Options for staging', 'Delete environment…'), dialog('Delete staging?')), full: false },

  // Chat apps (§12.3)
  { name: 'slack-manage', route: '/settings/connections/slack', setup: steps(see('Group chats')) },
  { name: 'slack-manage-who', route: '/settings/connections/slack', setup: steps(see('Who can use Bothy'), (p) => p.getByRole('heading', { name: 'Who can use Bothy' }).scrollIntoViewIfNeeded()), full: false },
  { name: 'slack-test', route: '/settings/connections/slack', setup: steps(click('Test connection'), see('The connection works.')) },
  { name: 'slack-test-failed', route: '/settings/connections/slack', mock: 'channel-fail:slack', setup: steps(click('Test connection'), see("didn't accept")) },
  {
    name: 'slack-test-replace',
    route: '/settings/connections/slack',
    mock: 'channel-fail:slack',
    setup: steps(click('Test connection'), click('Replace the tokens'), see('Paste the two tokens'), click('Replace the bot token')),
  },
  { name: 'slack-disconnect', route: '/settings/connections/slack', setup: steps(click('Disconnect Slack…'), dialog('Disconnect Slack?')), full: false },
  { name: 'slack-steps', route: '/settings/connections/slack', setup: steps(...disconnect('Slack'), see("Add Bothy's picture")) },
  { name: 'slack-manage-picture', route: '/settings/connections/slack', setup: steps(see('Download picture'), (p) => p.getByRole('link', { name: 'Download picture' }).scrollIntoViewIfNeeded()), full: false },
  { name: 'slack-checking', route: '/settings/connections/slack', mock: 'slow', setup: steps(...disconnect('Slack'), ...slackTokens, see('Checking the tokens…')) },
  { name: 'slack-connected', route: '/settings/connections/slack', setup: steps(...disconnect('Slack'), ...slackTokens, see('Connected to Acme as @bothy.'), see('Message Bothy in Slack to try it.')) },
  { name: 'slack-failed', route: '/settings/connections/slack', mock: 'channel-fail:slack', setup: steps(...disconnect('Slack'), ...slackTokens, see("Slack didn't accept"), click('Details')) },
  { name: 'slack-rejected-saved', route: '/settings/connections/slack', mock: 'channel-down:slack', setup: steps(see('saved'), see('Check again')) },
  { name: 'telegram-manage', route: '/settings/connections/telegram', setup: steps(see('Ops team')) },
  { name: 'telegram-manage-who', route: '/settings/connections/telegram', setup: steps(see('People you let in from Approvals.'), (p) => p.getByText('People you let in from Approvals.').scrollIntoViewIfNeeded()), full: false },
  { name: 'telegram-steps', route: '/settings/connections/telegram', setup: steps(...disconnect('Telegram')) },
  { name: 'telegram-connected', route: '/settings/connections/telegram', setup: steps(...disconnect('Telegram'), fill('Bot token', TELEGRAM, true), see('Connected as @acme_bothy_bot.'), see('Open Telegram')) },
  { name: 'telegram-failed', route: '/settings/connections/telegram', mock: 'channel-fail:telegram', setup: steps(...disconnect('Telegram'), fill('Bot token', TELEGRAM, true), see("Telegram didn't accept")) },
  { name: 'discord-steps', route: '/settings/connections/discord', setup: steps(see('Create a bot in the Discord developer portal')) },
  {
    name: 'discord-connected',
    route: '/settings/connections/discord',
    setup: steps(fill('Bot token', DISCORD), fill('Application ID', '112233445566778899', true), see('Add Bothy to Discord'), see('Message Bothy in Discord, then let yourself in from Approvals.')),
  },
  { name: 'discord-manage', route: '/settings/connections/discord', mock: 'discord-on', setup: steps(see('Acme Community')) },
  {
    name: 'discord-turn-off-confirm',
    route: '/settings/connections/discord',
    mock: 'discord-on',
    setup: steps(click('Answers here', 'switch' as 'button'), (p) => p.waitForTimeout(300), click('Answers here', 'switch' as 'button'), dialog('Turn off Bothy in #general?')),
    full: false,
  },
  { name: 'slack-delete-channel', route: '/settings/connections/slack', setup: steps(click('Delete #support…'), dialog('Delete #support?')), full: false },
  { name: 'discord-intent', route: '/settings/connections/discord', mock: 'channel-fail:intent', setup: steps(fill('Bot token', DISCORD), fill('Application ID', '112233445566778899', true), see('Message Content Intent turned on')) },
  { name: 'discord-failed', route: '/settings/connections/discord', mock: 'channel-fail:discord', setup: steps(fill('Bot token', DISCORD), fill('Application ID', '112233445566778899', true), see("Discord didn't accept")) },

  // MCP servers (§12.4)
  { name: 'mcp-server', route: '/settings/connections/mcp-linear', setup: steps(see('Ready · 12 actions')) },
  { name: 'mcp-server-details', route: '/settings/connections/mcp-linear', setup: steps(click('Details')), full: false },
  { name: 'mcp-server-menu', route: '/settings/connections/mcp-linear', setup: steps(click('Options for Linear')), full: false },
  { name: 'mcp-needs-review', route: '/settings/connections/mcp-docs', setup: steps(see('needs a review')) },
  { name: 'mcp-review', route: '/settings/connections/mcp-docs?review=1', setup: steps(dialog('Review changes to Docs')), full: false },
  { name: 'mcp-off', route: '/settings/connections/mcp-notes', setup: steps(see('Turn it on to see what it can do.')) },
  { name: 'mcp-error', route: '/settings/connections/mcp-linear', mock: 'mcp-error', setup: steps(see("Can't start"), click('Details')) },
  { name: 'mcp-installing', route: '/settings/connections/mcp-sentry', mock: 'mcp-installing', setup: steps(see('Installing…')) },
  { name: 'mcp-remove', route: '/settings/connections/mcp-linear', setup: steps(menu('Options for Linear', 'Remove server…'), dialog('Remove Linear?')), full: false },
  { name: 'mcp-missing', route: '/settings/connections/mcp-nope', setup: steps(see('This tool was removed or never added.')) },

  // Web search (§12.5)
  { name: 'search-off', route: '/settings/connections/search', setup: steps(see('Private search you host yourself')) },
  { name: 'search-searxng', route: '/settings/connections/search', setup: steps(click('SearXNG', 'radio'), see('Search works.')) },
  { name: 'search-brave', route: '/settings/connections/search', setup: steps(click('Brave Search', 'radio'), see('Search works.')) },
  { name: 'search-tavily', route: '/settings/connections/search', setup: steps(click('Tavily', 'radio')) },
  { name: 'search-tavily-rejected', route: '/settings/connections/search', setup: steps(click('Tavily', 'radio'), fill('API key', 'tvly-bad-0123456789abcdef', true), see("That key wasn't accepted")) },
  { name: 'search-private', route: '/settings/connections/search', mock: 'search-private', setup: steps(click('SearXNG', 'radio'), see('Allow searxng:8080')) },
  { name: 'search-unreachable', route: '/settings/connections/search', mock: 'search-fail:unreachable', setup: steps(click('SearXNG', 'radio'), see("isn't answering")) },
];

shots('connections', states);
