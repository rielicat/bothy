#!/usr/bin/env node
// Prints the newest Chromium in the Playwright cache, for when the revision
// @playwright/test pins is not installed (offline machines, older caches):
//
//   PLAYWRIGHT_CHROMIUM="$(node e2e/lib/chromium.mjs)" npm run smoke
//
// The headless shell is preferred: same engine, faster start.
import { existsSync, readdirSync, statSync } from 'node:fs';
import { homedir, platform } from 'node:os';
import { join } from 'node:path';

const cache =
  process.env.PLAYWRIGHT_BROWSERS_PATH ||
  (platform() === 'darwin' ? join(homedir(), 'Library/Caches/ms-playwright') : platform() === 'win32' ? join(process.env.LOCALAPPDATA ?? homedir(), 'ms-playwright') : join(homedir(), '.cache/ms-playwright'));

const binaries = ['chrome-headless-shell', 'chrome-headless-shell.exe', 'Google Chrome for Testing', 'Chromium', 'chrome', 'chrome.exe'];

function find(dir, depth) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const s = statSync(p);
    if (s.isFile() && binaries.includes(name)) return p;
    if (s.isDirectory() && depth > 0) {
      const hit = find(p, depth - 1);
      if (hit) return hit;
    }
  }
  return null;
}

const revision = (d) => Number(d.split('-').pop());
const installs = existsSync(cache)
  ? readdirSync(cache)
      .filter((d) => /^chromium(_headless_shell)?-\d+$/.test(d) && existsSync(join(cache, d, 'INSTALLATION_COMPLETE')))
      .sort((a, b) => revision(b) - revision(a) || Number(b.includes('headless')) - Number(a.includes('headless')))
  : [];

for (const d of installs) {
  const bin = find(join(cache, d), 5);
  if (bin) {
    process.stdout.write(bin + '\n');
    process.exit(0);
  }
}
process.stderr.write(`no Chromium found in ${cache}; run npx playwright install chromium\n`);
process.exit(1);
