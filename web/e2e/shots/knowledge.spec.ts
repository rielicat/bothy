// C5 Knowledge (docs/design/ui.md §9, §18.3): every route and state the chunk
// owns, at 1280 × 800 and 375 × 812, light and dark, with the G4 probes.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/knowledge.spec.ts
//   open e2e/shots/out/knowledge/index.html
import type { Page } from '@playwright/test';
import { shots, type ShotState } from '../lib/shots';

// Until C1 lands, pages render inside the v0.1 shell: a 240 px sidebar, a
// blurred top bar and a main area with its own scroll. This puts them in the
// v0.2 frame instead (document scroll, content after a 256 px sidebar), so the
// captures and probes judge the Knowledge screens, not the old shell.
const V02_FRAME = `
  nav[aria-label="Main"], div:has(> #main) > header, [role="region"][aria-label="Notices"] { display: none !important; backdrop-filter: none !important; }
  div:has(> nav[aria-label="Main"]), div:has(> #main) { display: block !important; height: auto !important; overflow: visible !important; }
  #main { overflow: visible !important; }
  @media (min-width: 1024px) { #main { margin-left: 256px; } }
`;

const framed =
  (text?: string, then?: (page: Page) => Promise<unknown>) =>
  async (page: Page) => {
    // A constructed sheet: the preview's CSP refuses <style> tags, CSSOM is allowed.
    await page.evaluate((css) => {
      // Only around the v0.1 shell; C1's shell is part of what the captures judge.
      if (!document.querySelector('div:has(> #main) > header')) return;
      const sheet = new CSSStyleSheet();
      sheet.replaceSync(css);
      document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
    }, V02_FRAME);
    await page.locator('h1').first().waitFor();
    if (text) await page.getByText(text, { exact: false }).filter({ visible: true }).first().waitFor();
    await then?.(page);
  };

const button = (name: string | RegExp) => (page: Page) => page.getByRole('button', { name, exact: typeof name === 'string' }).first().click();

const edit = async (page: Page) => {
  await button('Edit')(page);
  await page.locator('textarea').first().waitFor();
};

// The Memory page has an Edit per section; "Remembered for everyone" comes first.
const editIn = (region: string) => async (page: Page) => {
  const r = page.getByRole('region', { name: region });
  await r.getByRole('button', { name: 'Edit' }).click();
  await r.locator('textarea').waitFor();
};

const typeAtEnd = (text: string) => async (page: Page) => {
  const t = page.locator('textarea').first();
  await t.focus();
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.type(text);
};

const settle = (page: Page) => page.waitForTimeout(350);

const PROJECT = '/knowledge/projects/acme/web';
const SEARCH = '/knowledge/search?q=login%20redirect';

const states: ShotState[] = [
  // §9.1 Projects
  { name: 'projects', route: '/knowledge', ready: framed('acme/infra') },
  { name: 'projects-member', route: '/knowledge', mock: 'member', ready: framed('acme/infra') },
  {
    name: 'projects-failed-details',
    route: '/knowledge',
    mock: 'repo-read-failed',
    ready: framed('acme/infra', async (page) => {
      await button('Details')(page);
      await settle(page);
    }),
  },
  { name: 'empty-no-github-admin', route: '/knowledge', mock: 'knowledge-empty,github-none', ready: framed('Connect GitHub to give Bothy your code.') },
  { name: 'empty-no-github-member', route: '/knowledge', mock: 'knowledge-empty,github-none,member', ready: framed('Ask an admin') },
  { name: 'empty-nothing-chosen', route: '/knowledge', mock: 'knowledge-empty,github-no-repos', ready: framed('Choose which repositories') },

  // §9.2 Project page
  { name: 'project', route: PROJECT, ready: framed('How to build and test') },
  { name: 'project-member', route: PROJECT, mock: 'member', ready: framed('How to build and test') },
  { name: 'project-first-read', route: '/knowledge/projects/acme/api', ready: framed('Bothy is reading acme/api') },
  { name: 'project-couldnt-read', route: '/knowledge/projects/acme/infra', mock: 'repo-read-failed', ready: framed('GitHub says Bothy no longer has access to this repository.') },
  { name: 'project-too-big', route: '/knowledge/projects/acme/infra', ready: framed('This repository is too big for Bothy to read') },
  {
    name: 'project-menu',
    route: PROJECT,
    full: false,
    ready: framed('How to build and test', async (page) => {
      await page.getByRole('button', { name: 'Project options' }).click();
      await page.getByRole('menu').waitFor();
      await settle(page);
    }),
  },
  { name: 'project-edit', route: PROJECT, ready: framed('How to build and test', edit) },
  {
    name: 'project-discard',
    route: PROJECT,
    full: false,
    ready: framed('How to build and test', async (page) => {
      await edit(page);
      await typeAtEnd('\nA new line.')(page);
      await page.keyboard.press('Escape');
      await page.getByRole('dialog', { name: 'Discard your changes?' }).waitFor();
      await settle(page);
    }),
  },
  {
    name: 'project-conflict',
    route: PROJECT,
    mock: 'notes-conflict',
    ready: framed('How to build and test', async (page) => {
      await edit(page);
      await typeAtEnd('\n- Keep pull requests small.')(page);
      await button('Save')(page);
      await page.getByText('Someone else changed these notes while you were editing.').waitFor();
    }),
  },
  {
    name: 'project-conflict-compare',
    route: PROJECT,
    mock: 'notes-conflict',
    full: false,
    ready: framed('How to build and test', async (page) => {
      await edit(page);
      await typeAtEnd('\n- Keep pull requests small.')(page);
      await button('Save')(page);
      await button('Compare versions')(page);
      await page.getByRole('dialog', { name: 'Compare versions' }).waitFor();
      await settle(page);
    }),
  },
  { name: 'project-near-full', route: PROJECT, mock: 'notes-near-full', ready: framed('These notes are almost full.') },
  {
    name: 'project-too-long',
    route: PROJECT,
    mock: 'notes-near-full',
    ready: framed('These notes are almost full.', async (page) => {
      await edit(page);
      await typeAtEnd(`\n${'More detail about the build. '.repeat(16)}`)(page);
      await button('Save')(page);
      await page.getByText('These notes are too long to save.').waitFor();
    }),
  },
  {
    name: 'project-history',
    route: PROJECT,
    full: false,
    ready: framed('How to build and test', async (page) => {
      await button('History')(page);
      const d = page.getByRole('dialog', { name: 'History' });
      await d.waitFor();
      await d.getByRole('button', { name: 'Show' }).nth(1).click();
      await settle(page);
    }),
  },
  { name: 'project-suggestions', route: PROJECT, mock: 'notes-suggestions', ready: framed('Bothy suggested 2 changes to these notes.') },
  {
    name: 'project-suggestions-dialog',
    route: PROJECT,
    mock: 'notes-suggestions',
    full: false,
    // The cards are C4's: until C4 lands they are the v0.1 card, whose pill and bordered diff nest inside it.
    probes: { nesting: false },
    ready: framed('Bothy suggested 2 changes to these notes.', async (page) => {
      await button('Review')(page);
      await page.getByRole('dialog', { name: 'Suggested changes' }).waitFor();
      await settle(page);
    }),
  },
  {
    name: 'project-technical-details',
    route: PROJECT,
    full: false,
    ready: framed('How to build and test', async (page) => {
      await page.getByRole('button', { name: 'Project options' }).click();
      await page.getByRole('menuitem', { name: 'Technical details' }).click();
      await page.getByRole('dialog', { name: 'Technical details' }).waitFor();
      await settle(page);
    }),
  },
  {
    name: 'project-stop-confirm',
    route: PROJECT,
    full: false,
    ready: framed('How to build and test', async (page) => {
      await page.getByRole('button', { name: 'Project options' }).click();
      await page.getByRole('menuitem', { name: 'Stop reading this repository…' }).click();
      await page.getByRole('dialog', { name: 'Stop reading acme/web?' }).waitFor();
      await settle(page);
    }),
  },

  // §9.3 Search
  { name: 'search-all', route: SEARCH, ready: framed('src/auth/redirect.ts') },
  { name: 'search-code', route: `${SEARCH}&type=code`, ready: framed('src/auth/redirect.ts') },
  { name: 'search-notes', route: `${SEARCH}&type=notes`, ready: framed('Project notes') },
  { name: 'search-repo', route: `${SEARCH}&repo=acme/docs`, ready: framed('sign-in.md') },
  { name: 'search-none', route: '/knowledge/search?q=kubernetes%20operator', ready: framed('Nothing in your projects or notes matches') },
  {
    name: 'search-details',
    route: `${SEARCH}&type=code`,
    ready: framed('src/auth/redirect.ts', async (page) => {
      await button('Details')(page);
      await settle(page);
    }),
  },

  // §9.4 Memory
  { name: 'memory', route: '/knowledge/memory', ready: framed('Things Bothy remembers') },
  { name: 'memory-edit', route: '/knowledge/memory', ready: framed('Things Bothy remembers', editIn('About you')) },
  { name: 'memory-everyone-edit', route: '/knowledge/memory', ready: framed('Things Bothy remembers', editIn('Remembered for everyone')) },
  { name: 'memory-near-full', route: '/knowledge/memory', mock: 'memory-near-full', ready: framed('This is almost full.') },
  {
    name: 'memory-saved',
    route: '/knowledge/memory',
    ready: framed('Things Bothy remembers', async (page) => {
      await editIn('About you')(page);
      await typeAtEnd(' Based in Lisbon.')(page);
      await button('Save')(page);
      await page.getByText('Saved', { exact: true }).waitFor();
    }),
  },
  {
    name: 'memory-applies',
    route: '/knowledge/memory',
    ready: framed('Things Bothy remembers', async (page) => {
      await editIn('About you')(page);
      await typeAtEnd(' Based in Lisbon.')(page);
      await button('Save')(page);
      await page.getByText('Changes apply from your next message.').waitFor({ timeout: 5000 });
    }),
  },
  { name: 'memory-empty', route: '/knowledge/memory', mock: 'memory-empty', ready: framed('Bothy adds things here as you work together.') },
  { name: 'memory-member', route: '/knowledge/memory', mock: 'member', ready: framed('Only admins can change it.') },
  { name: 'memory-everyone-empty', route: '/knowledge/memory', mock: 'team-memory-empty', ready: framed('like your deploy day.') },
  { name: 'memory-everyone-empty-member', route: '/knowledge/memory', mock: 'team-memory-empty,member', ready: framed('an admin can approve it.') },
  {
    name: 'memory-history',
    route: '/knowledge/memory',
    full: false,
    ready: framed('Things Bothy remembers', async (page) => {
      await button('History')(page);
      const d = page.getByRole('dialog', { name: 'History' });
      await d.waitFor();
      await d.getByRole('button', { name: 'Show' }).first().click();
      await settle(page);
    }),
  },

  // §9.5 Skills
  { name: 'skills', route: '/knowledge/skills', ready: framed('Incident review') },
  { name: 'skills-member', route: '/knowledge/skills', mock: 'member', ready: framed('Incident review') },
  { name: 'skills-empty', route: '/knowledge/skills', mock: 'skills-empty', ready: framed('No skills yet.') },
  { name: 'skills-empty-member', route: '/knowledge/skills', mock: 'skills-empty,member', ready: framed('No skills yet.') },
  { name: 'skill', route: '/knowledge/skills/release-notes', ready: framed('Find the last release tag') },
  {
    name: 'skill-files',
    route: '/knowledge/skills/release-notes',
    ready: framed('Find the last release tag', async (page) => {
      await button('Files')(page);
      await page.getByRole('button', { name: 'scripts/collect.sh' }).click();
      await page.getByText('git log --merges').waitFor();
    }),
  },
  { name: 'skill-changed', route: '/knowledge/skills/incident-review', ready: framed("This skill changed, so it's off until an admin approves") },
];

shots('knowledge', states);
