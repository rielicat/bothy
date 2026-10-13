import { expect, test } from '@playwright/test';

// The gateway keeps four event streams per user and closes the oldest, so
// tabs share one: five tabs open a single /api/events stream, a tab that does
// not hold it still sees the run it started, and when the streaming tab
// closes another takes over. The session and the message are ones the other
// specs do not use, since the fixture gateway's state is shared.
test('tabs share one event stream', async ({ context }) => {
  let streams = 0;
  context.on('request', (r) => {
    if (new URL(r.url()).pathname === '/api/events') streams++;
  });
  const pages = [];
  for (let i = 0; i < 5; i++) {
    const p = await context.newPage();
    await p.goto('/chat/ses_01jrecipe0000000000000000');
    pages.push(p);
  }
  await pages[4].waitForTimeout(1000);
  expect(streams).toBe(1);

  const follower = pages[4];
  await follower.getByRole('textbox', { name: 'Message' }).fill('Which flour works best for focaccia?');
  await follower.keyboard.press('Enter');
  await expect(follower.getByText('Let me check a couple of sources.')).toBeVisible({ timeout: 10_000 });

  await pages[0].close();
  await expect.poll(() => streams, { timeout: 5_000 }).toBe(2);
});
