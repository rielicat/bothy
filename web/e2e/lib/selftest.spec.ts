// Checks the helpers every shot spec relies on: the G4 probes, the G6 copy
// rules, the phone emulation and the mock's scenario switches.
import { expect, test } from '@playwright/test';
import { plainCopyProblems } from './copy';
import { copyMatches } from './copy-patterns';
import { openPage, probe, viewports } from './shots';

const page1280 = { width: 1280, height: 800 };

test.describe('layout probes', () => {
  test('pass on a page that follows the rules', async ({ page }) => {
    await page.setViewportSize(page1280);
    await page.setContent(`
      <style>:root{--elevation-card:0 1px 2px rgb(74 58 33 / .05), 0 6px 16px -8px rgb(74 58 33 / .12)}</style>
      <body style="margin:0;font:14px system-ui">
        <main style="width:720px;margin-left:408px">
          <h1 style="font-size:20px;margin:0">Approvals</h1>
          <div style="border:1px solid #d3cfc6;padding:16px;box-shadow:0 0 #0000, var(--elevation-card)">
            <p>Delete notes/staging.md?</p>
            <input style="border:1px solid #8f8a80;height:36px"> <button style="border:1px solid #d3cfc6;height:36px">Deny</button>
            <a href="#" style="border:1px solid #ccc;display:inline-block;height:36px">Details</a>
            <p>Read the <a href="#">guide</a> first.</p>
          </div>
          <div style="border-bottom:1px solid #e6e3dc">a row with a hairline</div>
          <span style="position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0);font-size:8px">screen reader text</span>
          <div role="menu" style="border:1px solid #e6e3dc;box-shadow:0 8px 24px rgb(0 0 0 / .12)"><div style="border:1px solid #eee">item</div></div>
          <div data-floating style="box-shadow:0 8px 24px rgb(0 0 0 / .12)">toast</div>
          <div style="box-shadow:0 0 #0000">no shadow</div>
        </main>
      </body>`);
    expect(await probe(page)).toEqual([]);
  });

  test('catch every G4 problem', async ({ page }) => {
    await page.setViewportSize(page1280);
    await page.setContent(`
      <body style="margin:0;font:14px system-ui">
        <main style="margin-left:300px;width:1200px">
          <h1>Settings</h1>
          <p style="font-size:11px">Tiny meta</p>
          <div style="backdrop-filter:blur(4px)">frosted</div>
          <div style="box-shadow:0 1px 2px rgb(0 0 0 / .1)">card with a shadow that is no named elevation</div>
          <section style="border:1px solid #ccc"><div style="border:1px solid #ccc">card in a card</div></section>
        </main>
      </body>`);
    const problems = await probe(page);
    expect(problems.map((p) => p.split(':')[0])).toEqual(['overflow', 'left-edge', 'min-text', 'blur', 'shadow', 'nesting']);
    expect(problems[1]).toContain('x = 300');
    expect(problems[2]).toContain('11px');
  });

  test('measure the chat header from its [data-edge] box', async ({ page }) => {
    await page.setViewportSize(page1280);
    await page.setContent('<body style="margin:0"><header><div data-edge style="margin-left:408px;display:flex"><a href="#">Q3 notes</a><h1 style="margin:0 0 0 8px">Pricing</h1></div></header></body>');
    expect(await probe(page, { leftEdge: true, overflow: false, minText: false, blur: false, shadows: false, nesting: false, targets: false })).toEqual([]);
  });

  test('can be switched off one by one', async ({ page }) => {
    await page.setViewportSize(page1280);
    await page.setContent('<body style="margin:0"><h1 style="margin-left:24px">Set up Bothy</h1></body>');
    expect(await probe(page)).toEqual(['left-edge: the h1 starts at x = 24, not 408 (<h1> “Set up Bothy”)']);
    expect(await probe(page, { leftEdge: false })).toEqual([]);
  });

  test('allow a focus ring drawn with box-shadow', async ({ page }) => {
    await page.setViewportSize(page1280);
    await page.setContent(`<body style="margin:0"><h1 style="margin-left:408px">Chat</h1>
      <div id="composer" style="border:1px solid #2a6a54;box-shadow:0 0 0 1px #2a6a54"><textarea id="t"></textarea></div></body>`);
    await page.focus('#t');
    expect(await probe(page)).toEqual([]);
    await page.evaluate(() => (document.activeElement as HTMLElement).blur());
    expect(await probe(page)).toEqual([expect.stringContaining('shadow: a box-shadow that is no named elevation')]);
  });
});

test.describe('plain copy', () => {
  test('rules match the plan', () => {
    const rules = (s: string) => copyMatches(s).map((m) => m.rule);
    expect(rules('Your session expired')).toEqual(['banned-word']);
    expect(rules('Running code is off on this server.')).toEqual([]);
    expect(rules('2.3K of 33K tokens · 7%')).toEqual(['token-talk']);
    expect(rules('1,800 tokens in')).toEqual(['token-talk']);
    expect(rules('Create an access token')).toEqual([]);
    expect(rules('Saved!')).toEqual(['exclamation']);
    expect(rules('Chat ses_01jtrip000000000000000000')).toEqual(['id']);
    expect(rules('Untitled chat')).toEqual(['untitled-chat']);
    expect(rules('never')).toEqual(['never-as-time']);
    expect(rules('Last used: never')).toEqual(['never-as-time']);
    expect(rules('Bothy never sends data without asking.')).toEqual([]);
    expect(rules('Edit SOUL.md')).toEqual(['banned-word']);
    expect(rules('Every 30 min (cron)')).toEqual(['banned-word']);
  });

  test('read rendered text and labels, and skip Details and content', async ({ page }) => {
    await page.setContent(`
      <title>Approvals · Bothy</title>
      <main>
        <h1>Approvals</h1>
        <p>This chat holds untrusted session data.</p>
        <button aria-label="Approve now!">✓</button>
        <input placeholder="Message Bothy">
        <span class="meta">Last used: never</span>
        <div data-details><p>Rule default:local_write in session ses_01abc</p></div>
        <div class="md"><p>I can do that for you!</p></div>
        <code data-copy-ignore>rm -rf sessions/vault</code>
        <details><summary>Technical details</summary><p>2,300 tokens used</p></details>
        <p style="display:none">Untitled chat</p>
      </main>`);
    const found = await plainCopyProblems(page);
    expect(found.map((p) => `${p.rule} ${p.match}`)).toEqual(['banned-word session', 'never-as-time Last used: never', 'exclamation !']);
    expect(found[0].where).toBe('main > p');
    expect(found[2].where).toContain('[aria-label]');
    expect(await plainCopyProblems(page, { allow: [/untrusted session/, 'Approve now!', 'Last used: never'] })).toEqual([]);
  });
});

test('targets are 44 px under a coarse pointer, inline links and labelled boxes excepted', async ({ browser }, info) => {
  const page = await openPage(browser, info, viewports.phone, 'light');
  try {
    await page.setContent(`<meta name="viewport" content="width=device-width, initial-scale=1">
      <body style="margin:0">
        <a href="#main" style="position:absolute;width:1px;height:1px;padding:8px 12px;overflow:hidden;clip-path:inset(50%)">Skip to content</a>
        <button style="height:44px;width:120px">Delete file</button>
        <button style="height:32px;width:120px">Deny</button>
        <p>See <a href="#">the guide</a> for more.</p>
        <label style="display:flex;min-height:44px;align-items:center"><input type="checkbox"> Remember me</label>
        <input type="checkbox" id="lonely"><span>No label</span>
      </body>`);
    const found = await probe(page, { targets: true, overflow: false, leftEdge: false, minText: false, blur: false, shadows: false, nesting: false });
    expect(found).toHaveLength(2);
    expect(found[0]).toMatch(/^targets: under 44 px: 120×32 <button> “Deny”/);
    expect(found[1]).toMatch(/^targets: under 44 px: \d+×\d+ <input#lonely>/);
  } finally {
    await page.context().close();
  }
});

test('the phone shot is a touch device with a coarse pointer', async ({ browser }, info) => {
  const page = await openPage(browser, info, viewports.phone, 'dark');
  try {
    await page.setContent('<meta name="viewport" content="width=device-width, initial-scale=1"><p>x</p>');
    expect(await page.evaluate(() => [innerWidth, matchMedia('(pointer: coarse)').matches, matchMedia('(hover: hover)').matches, matchMedia('(prefers-color-scheme: dark)').matches])).toEqual([375, true, false, true]);
  } finally {
    await page.context().close();
  }
});

test('mock scenarios ride along from ?mock= to the API', async ({ page }) => {
  await page.goto('/chat?mock=paused,sealed');
  await expect(page.locator('#app')).not.toBeEmpty();
  const me = await page.request.get('/api/auth/me').then((r) => r.json());
  expect(me.paused).toBe(true);
  expect(me.banners.map((b: { code: string }) => b.code)).toEqual(expect.arrayContaining(['paused', 'vault_sealed']));
  const list = await page.request.get('/api/mock/scenarios').then((r) => r.json());
  expect(list.active).toEqual({ paused: '', sealed: '' });
  expect(list.scenarios.map((s: { name: string }) => s.name)).toEqual(expect.arrayContaining(['paused', 'member', 'detect-aws', 'provider-fail']));

  await page.goto('/chat?mock=');
  const after = await page.request.get('/api/auth/me').then((r) => r.json());
  expect(after.paused).toBe(false);
});
