// C1 shell and navigation (docs/design/ui.md §18.3): the sidebar, settings
// mode, the phone top bar and drawer, the account menu, banners, search, the
// shortcuts and pause dialogs, and the not-found and no-access pages.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/shell.spec.ts
//
// Most states sit on /activity, whose page is still a placeholder, so the
// captures judge the shell rather than a v0.1 screen inside it.
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

const TEAM = 'team-chats';
// One comma list rather than an array: the index page prints `mock` as text.
const m = (...names: string[]) => names.join(',');
const sidebar = (page: Page) => page.getByRole('navigation', { name: 'Main' });

// On phones the sidebar lives in the drawer; on wide screens it is always there.
async function inSidebar(page: Page, phone: boolean) {
  if (phone) {
    await page.getByRole('button', { name: /^Open menu/ }).click();
    const drawer = page.getByRole('dialog', { name: 'Menu' });
    await drawer.waitFor();
    return drawer;
  }
  return sidebar(page);
}

async function openAccountMenu(page: Page, phone: boolean) {
  const where = await inSidebar(page, phone);
  await where.getByRole('button', { name: /^Account:/ }).click();
  await page.getByRole('menu').first().waitFor();
}

// A CSS locator, not a role one: on phones the pinned sidebar is hidden, so its links aren't in the accessibility tree.
const chatsReady = (title = 'Fix login redirect') => async (page: Page) => {
  await page.locator('nav[aria-label="Main"] a', { hasText: title }).first().waitFor({ state: 'attached' });
};

const states: ShotState[] = [
  // Sidebar
  { name: 'sidebar', route: '/activity', mock: TEAM, ready: chatsReady() },
  { name: 'sidebar-plain', route: '/activity', mock: m(TEAM, 'chats-idle', 'no-banners', 'no-findings'), ready: chatsReady() },
  {
    name: 'sidebar-long',
    route: '/activity',
    mock: 'long-chats',
    ready: chatsReady(),
    setup: async (page, { viewport }) => {
      const where = await inSidebar(page, viewport.coarse);
      const more = where.getByRole('button', { name: 'Show more' });
      await more.scrollIntoViewIfNeeded();
    },
  },
  {
    name: 'sidebar-chat-menu',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page, { viewport }) => {
      const where = await inSidebar(page, viewport.coarse);
      await where.getByRole('link', { name: 'v2.3 launch checklist' }).hover();
      await where.getByRole('button', { name: 'Options for v2.3 launch checklist' }).click();
      await page.getByRole('menu').waitFor();
    },
  },
  {
    name: 'chat-rename',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page, { viewport }) => {
      const where = await inSidebar(page, viewport.coarse);
      await where.getByRole('link', { name: 'Q3 pricing notes' }).hover();
      await where.getByRole('button', { name: 'Options for Q3 pricing notes' }).click();
      await page.getByRole('menuitem', { name: 'Rename' }).click();
      await page.getByRole('dialog', { name: 'Rename chat' }).waitFor();
    },
  },
  {
    name: 'chat-delete',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page, { viewport }) => {
      const where = await inSidebar(page, viewport.coarse);
      await where.getByRole('link', { name: 'Q3 pricing notes' }).hover();
      await where.getByRole('button', { name: 'Options for Q3 pricing notes' }).click();
      await page.getByRole('menuitem', { name: 'Delete chat…' }).click();
      await page.getByRole('dialog', { name: 'Delete this chat?' }).waitFor();
    },
  },
  { name: 'sidebar-member', route: '/activity', mock: m(TEAM, 'member'), ready: chatsReady() },

  // Settings mode (§4.3) and the phone list of groups
  { name: 'settings-admin', route: '/settings/connections/github', mock: TEAM },
  // The page inside is still the v0.1 settings frame (C7), so only the sidebar is judged.
  { name: 'settings-member', route: '/settings/account', mock: m(TEAM, 'member'), probes: { leftEdge: false, nesting: false, targets: false } },
  { name: 'settings-list', route: '/settings', mock: TEAM, only: { viewports: ['phone'] } },
  {
    name: 'settings-drawer',
    route: '/settings/connections/github',
    mock: TEAM,
    only: { viewports: ['phone'] },
    setup: (page) => inSidebar(page, true),
  },
  { name: 'settings-list-member', route: '/settings', mock: m(TEAM, 'member'), only: { viewports: ['phone'] } },

  // Phone and tablet top bar and drawer (§4.4)
  { name: 'topbar', route: '/activity', mock: TEAM, ready: chatsReady(), only: { viewports: ['phone'] } },
  { name: 'topbar-nothing-waiting', route: '/activity', mock: m(TEAM, 'chats-idle'), only: { viewports: ['phone'] } },
  {
    name: 'topbar-scrolled',
    route: '/activity',
    mock: TEAM,
    only: { viewports: ['phone'] },
    full: false,
    setup: async (page) => {
      // A long page, so the title moves up into the bar.
      await page.evaluate(() => {
        const tall = document.createElement('div');
        tall.style.height = '2000px';
        document.querySelector('main .page')?.append(tall);
        window.scrollTo(0, 400);
      });
      await page.waitForTimeout(200);
    },
  },
  {
    name: 'drawer',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    only: { viewports: ['phone'] },
    setup: (page) => inSidebar(page, true),
  },

  // Account menu (§4.2)
  { name: 'account-menu', route: '/activity', mock: TEAM, ready: chatsReady(), setup: (page, { viewport }) => openAccountMenu(page, viewport.coarse) },
  {
    name: 'account-menu-theme',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page, { viewport }) => {
      await openAccountMenu(page, viewport.coarse);
      await page.getByRole('menuitem', { name: 'Theme' }).click();
      await page.getByRole('menu', { name: 'Theme' }).waitFor();
    },
  },
  {
    name: 'pause-dialog',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page, { viewport }) => {
      await openAccountMenu(page, viewport.coarse);
      await page.getByRole('menuitem', { name: 'Pause Bothy…' }).click();
      await page.getByRole('dialog', { name: 'Pause Bothy?' }).waitFor();
    },
  },

  // Banners (§4.9), one per condition
  { name: 'banner-paused', route: '/activity', mock: m(TEAM, 'paused') },
  { name: 'banner-paused-member', route: '/activity', mock: m(TEAM, 'paused', 'member') },
  { name: 'banner-sealed', route: '/settings/connections/github', mock: m(TEAM, 'sealed') },
  { name: 'unlock-screen', route: '/activity', mock: m(TEAM, 'sealed'), ready: (page) => page.getByRole('heading', { level: 1, name: 'Unlock Bothy' }).waitFor() },
  { name: 'banner-provider', route: '/activity', mock: m(TEAM, 'provider-auth') },
  { name: 'banner-safe-mode', route: '/activity', mock: m(TEAM, 'safe-mode') },
  { name: 'banner-spend-limit', route: '/activity', mock: m(TEAM, 'spend-limit'), ready: 'spending limit' },
  { name: 'banner-restart', route: '/activity', mock: m(TEAM, 'restart') },

  // Search (§6.3)
  {
    name: 'search',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page) => {
      await page.keyboard.press('/');
      await page.getByRole('combobox', { name: 'Search chats' }).waitFor();
      await page.getByRole('option').first().waitFor();
    },
  },
  {
    name: 'search-results',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page) => {
      await page.keyboard.press('/');
      await page.getByRole('combobox', { name: 'Search chats' }).fill('launch');
      await page.getByRole('listbox', { name: 'Chats' }).getByRole('option').first().waitFor();
      await page.waitForTimeout(250);
    },
  },
  {
    name: 'search-none',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page) => {
      await page.keyboard.press('/');
      await page.getByRole('combobox', { name: 'Search chats' }).fill('zebra crossing');
      await page.getByRole('listbox').getByText('No results for “zebra crossing”.').waitFor();
    },
  },

  // Keyboard shortcuts (§6.4)
  {
    name: 'shortcuts',
    route: '/activity',
    mock: TEAM,
    ready: chatsReady(),
    setup: async (page) => {
      await page.keyboard.press('Shift+Slash');
      await page.getByRole('dialog', { name: 'Keyboard shortcuts' }).waitFor();
    },
  },

  // Not found and no access (§13.1)
  { name: 'not-found', route: '/nope', mock: TEAM },
  { name: 'no-access', route: '/activity/audit', mock: m(TEAM, 'member') },
  { name: 'no-access-settings', route: '/settings/security', mock: m(TEAM, 'member') },
];

shots('shell', states);
