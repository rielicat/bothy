import { defineConfig, devices } from '@playwright/test';

// UI smoke test against the production build served by `vite preview` with
// the fixture gateway and the gateway's CSP (npm run build, then npm run smoke).
// PLAYWRIGHT_CHROMIUM points at a local Chromium when the pinned one is not installed.
const port = 5197;

export default defineConfig({
  testDir: 'e2e',
  fullyParallel: false,
  workers: 1,
  reporter: 'list',
  webServer: {
    command: `npm run preview:mock -- --port ${port} --strictPort`,
    port,
    reuseExistingServer: !process.env.CI,
  },
  use: {
    baseURL: `http://localhost:${port}`,
    launchOptions: process.env.PLAYWRIGHT_CHROMIUM ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM } : {},
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
