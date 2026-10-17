// C6 Activity (docs/design/ui.md §10, §18.3): every route and state the chunk
// owns, at 1280 × 800 and 360 × 780 in light and dark, with the G4 probes.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/activity.spec.ts
//   open e2e/shots/out/activity/index.html
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

// Until C1 replaces the v0.1 shell (a 240 px sidebar, a blurred top bar and a
// main area with its own scroll), this puts the page in the v0.2 frame: the
// document scrolls and the column starts after a 256 px sidebar, so the
// captures and probes judge Activity, not the old shell.
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

// A constructed stylesheet, because the production CSP refuses <style> tags.
async function frame(page: Page) {
  await page.evaluate((css) => {
    // Only around the v0.1 shell; C1's shell is part of what the captures judge.
    if (!document.querySelector('div:has(> #main) > header')) return;
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }, V02_FRAME);
  await page.locator('h1').first().waitFor();
}

const then =
  (...steps: ((page: Page) => Promise<unknown>)[]) =>
  async (page: Page) => {
    await frame(page);
    for (const s of steps) await s(page);
    await page.waitForTimeout(250);
  };
// A DOM click: with the frame above on a phone, Chromium's hit test can land on
// the content above a target near the bottom of a long page.
const click = (name: string | RegExp, opts: { exact?: boolean } = {}) => (page: Page) =>
  page
    .getByRole('button', { name, ...opts })
    .first()
    .evaluate((el) => (el as HTMLElement).click());
const pick = (label: string, value: string) => (page: Page) => page.getByLabel(label, { exact: true }).selectOption(value);

const states: ShotState[] = [
  // Tasks (§10.1)
  { name: 'tasks', route: '/activity', ready: 'Fix login redirect', setup: frame },
  { name: 'tasks-member', route: '/activity', mock: 'member', ready: 'Draft the release notes', setup: frame },
  { name: 'tasks-filter-attention', route: '/activity?result=attention', ready: 'Waiting for approval', setup: frame },
  { name: 'tasks-filter-apps', route: '/activity?where=apps', ready: 'Draft the release notes', setup: frame },
  { name: 'tasks-filtered-empty', route: '/activity?where=api&result=failed', ready: 'Nothing matches these filters.', setup: frame },
  { name: 'tasks-empty', route: '/activity', mock: 'activity-empty', ready: 'Nothing yet.', setup: frame },
  { name: 'tasks-votes', route: '/activity', mock: 'deploy-waiting', ready: '(1 of 2)', setup: frame },

  // Task detail (§10.2)
  { name: 'task', route: '/activity/tasks/run_01jfixlogin0000000000000', ready: 'What Bothy did', setup: frame },
  {
    name: 'task-details-open',
    route: '/activity/tasks/run_01jfixlogin0000000000000',
    ready: 'What Bothy did',
    full: true,
    setup: then(click(/^Changed/), click('Technical details')),
  },
  { name: 'task-refined', route: '/activity/tasks/run_01jpricing00000000000000', ready: 'Refined from', full: true, setup: then(click('Technical details')) },
  { name: 'task-waiting', route: '/activity/tasks/run_01jdeployweb000000000000', ready: 'Waiting for approval to deploy', setup: frame },
  { name: 'task-failed', route: '/activity/tasks/run_01jprdigest1000000000000', ready: "Couldn't check builds", setup: frame },
  { name: 'task-not-found', route: '/activity/tasks/run_missing', ready: "This task doesn't exist or was deleted.", setup: frame },

  // Schedules (§10.3)
  { name: 'schedules', route: '/activity/schedules', ready: 'Weekly PR digest', setup: frame },
  { name: 'schedules-empty', route: '/activity/schedules', mock: 'activity-empty', ready: 'Bothy can do things on a schedule', setup: frame },
  { name: 'schedules-menu', route: '/activity/schedules', ready: 'Weekly PR digest', full: false, setup: then(click('More for Weekly PR digest')) },
  {
    name: 'schedules-delete',
    route: '/activity/schedules',
    ready: 'Weekly PR digest',
    full: false,
    setup: then(click('More for Weekly PR digest'), (p) => p.getByRole('menuitem', { name: 'Delete…' }).evaluate((el) => (el as HTMLElement).click())),
  },
  { name: 'schedules-team', route: '/activity/schedules', ready: 'Weekly sales summary', setup: then((p) => p.getByRole('heading', { name: 'In chat apps' }).scrollIntoViewIfNeeded()) },
  { name: 'schedules-team-delete', route: '/activity/schedules', ready: 'Weekly sales summary', full: false, setup: then(click('Delete Ops handover…')) },
  { name: 'schedule-new', route: '/activity/schedules/new', ready: 'What should Bothy do?', setup: frame },
  { name: 'schedule-new-errors', route: '/activity/schedules/new', ready: 'What should Bothy do?', setup: then(click('Create schedule')) },
  {
    name: 'schedule-custom',
    route: '/activity/schedules/new',
    ready: 'What should Bothy do?',
    setup: then(pick('Repeat', 'custom'), (p) => p.getByLabel('Repeat rule').fill('every monday'), click('Create schedule')),
  },
  {
    name: 'schedule-more-options',
    route: '/activity/schedules/new',
    ready: 'What should Bothy do?',
    full: true,
    setup: then(
      (p) => p.getByLabel('What should Bothy do?').fill("Summarise yesterday's merged pull requests in acme/web and the status page at status.example.com."),
      pick('Repeat', 'weekly'),
      (p) => p.getByText(/^Runs Mondays .+ Next: /).waitFor(),
      click('More options'),
    ),
  },
  { name: 'schedule-edit', route: '/activity/schedules/sch_01jstandup00000000000000', ready: 'Recent runs', full: true, setup: frame },
  { name: 'schedule-edit-custom', route: '/activity/schedules/sch_01jinvoices0000000000000', ready: 'Repeat rule', setup: frame },
  { name: 'schedule-once', route: '/activity/schedules/new', ready: 'What should Bothy do?', setup: then(pick('Repeat', 'once')) },
  { name: 'checkin', route: '/activity/schedules/checkin', ready: 'What to check', setup: frame },

  // Releases (§10.4)
  { name: 'releases', route: '/activity/releases', ready: 'History', setup: frame },
  { name: 'releases-waiting-denied', route: '/activity/releases', mock: 'deploy-waiting', ready: 'Waiting for approval', setup: frame },
  { name: 'releases-empty', route: '/activity/releases', mock: 'releases-empty', ready: 'No deploys yet.', setup: frame },
  { name: 'releases-no-environments', route: '/activity/releases', mock: 'releases-none', ready: 'No deploy environments yet.', setup: frame },

  // Spending (§10.5)
  { name: 'spending-30', route: '/activity/spending', ready: 'in the last 30 days', setup: frame },
  { name: 'spending-7', route: '/activity/spending?range=7', ready: 'in the last 7 days', setup: frame },
  { name: 'spending-today', route: '/activity/spending?range=today', ready: 'today', setup: frame },
  { name: 'spending-50', route: '/activity/spending', mock: 'spend-50', ready: 'daily limit', setup: frame },
  { name: 'spending-80', route: '/activity/spending', mock: 'spend-80', ready: 'Bothy will stop starting new work', setup: frame },
  { name: 'spending-100', route: '/activity/spending', mock: 'spend-limit', ready: "Bothy reached today's limit.", setup: frame },
  { name: 'spending-chat-share-80', route: '/activity/spending', mock: 'chat-share:80', ready: 'Chat apps have used 85%', setup: frame },
  { name: 'spending-chat-share-out', route: '/activity/spending', mock: 'chat-share:out', ready: 'Chat apps used their', setup: frame },
  { name: 'spending-member', route: '/activity/spending', mock: 'member', ready: 'By model', setup: frame },
  { name: 'spending-empty', route: '/activity/spending', mock: 'activity-empty', ready: 'Nothing spent in this period.', setup: frame },
  { name: 'spending-details', route: '/activity/spending', ready: 'By model', full: true, setup: then(click('Technical details'), click('Show all')) },
  {
    name: 'spending-bar-focus',
    route: '/activity/spending?range=7',
    ready: 'in the last 7 days',
    full: false,
    only: { viewports: ['desktop'] },
    setup: then((p) => p.getByRole('img').nth(5).focus()),
  },

  // Audit log (§10.6)
  { name: 'audit', route: '/activity/audit', ready: 'Approved deleting', setup: frame },
  { name: 'audit-expanded', route: '/activity/audit', ready: 'Approved deleting', setup: then(click(/Approved deleting/), click('Raw record')) },
  { name: 'audit-intact', route: '/activity/audit', ready: 'Approved deleting', setup: then(click('Check the log'), (p) => p.getByText('The log is intact.').waitFor()) },
  {
    name: 'audit-broken',
    route: '/activity/audit',
    mock: 'audit-broken',
    ready: 'Approved deleting',
    setup: then(click('Check the log'), (p) => p.getByText("doesn't match the ones before it").waitFor()),
  },
  { name: 'audit-web', route: '/activity/audit?view=web', ready: 'Reached api.github.com', setup: frame },
  { name: 'audit-allow-site', route: '/activity/audit?view=web', ready: 'Reached api.github.com', full: false, setup: then(click('Allow this site…')) },
  { name: 'audit-filtered-empty', route: '/activity/audit?action=keys&period=today', ready: 'Nothing matches these filters.', setup: frame },
  { name: 'audit-member', route: '/activity/audit', mock: 'member', ready: 'Only admins can see this page.', setup: frame },
];

shots('activity', states);
