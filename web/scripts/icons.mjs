// Renders the PNG icons from the SVGs in public/ with Playwright's Chromium:
// the manifest's icons from the rounded tile, the Apple touch icon and Bothy's
// default chat-app picture (internal/picture) from the full-bleed one. The
// picture's default.sum records which icon-maskable.svg it came from, so its
// test notices a change that wasn't rendered. Run by `make icons`;
// PLAYWRIGHT_CHROMIUM points at a local Chromium as for the smoke test.
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { chromium } from '@playwright/test';

const pub = new URL('../public/', import.meta.url);
const picture = new URL('../../internal/picture/', import.meta.url);
const jobs = [
  ['icon.svg', 192, new URL('icon-192.png', pub)],
  ['icon.svg', 512, new URL('icon-512.png', pub)],
  ['icon-maskable.svg', 180, new URL('apple-touch-icon.png', pub)],
  ['icon-maskable.svg', 1024, new URL('default.png', picture)],
];

const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM || undefined });
try {
  const page = await browser.newPage();
  for (const [svg, size, out] of jobs) {
    await page.setViewportSize({ width: size, height: size });
    await page.setContent(`<style>body{margin:0}svg{display:block;width:${size}px;height:${size}px}</style>${readFileSync(new URL(svg, pub), 'utf8')}`);
    writeFileSync(out, await page.screenshot({ omitBackground: true }));
  }
} finally {
  await browser.close();
}

const sum = createHash('sha256').update(readFileSync(new URL('icon-maskable.svg', pub))).digest('hex');
writeFileSync(new URL('default.sum', picture), `${sum}  web/public/icon-maskable.svg\n`);
