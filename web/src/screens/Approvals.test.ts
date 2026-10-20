import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { recentApprovals } from '../../mock/fixtures';
import { resetMock } from '../../mock/server';
import { votes } from '../components/approval/votes.svelte';
import { live as region } from '../components/ui/live.svelte';
import { router } from '../lib/router.svelte';
import { approvals, session } from '../lib/stores/session.svelte';
import { installApi, type Call } from '../test/api';
import { FakeEventSource } from '../test/setup';
import Approvals from './Approvals.svelte';

const DEPLOY = 'apr_01jdeployweb0000000000000';
const SHELL = 'apr_01jshell00000000000000000';
const PR = 'apr_01jpr00000000000000000000';

let calls: Call[];
const posts = () => calls.filter((c) => c.method === 'POST' && c.url.startsWith('/api/approvals/'));
// The shell (App) mounts the live region; announce() writes to its store.
const live = () => region.polite;
const expanded = () => document.querySelector<HTMLElement>('[data-row] article');
const row = (id: string) => document.querySelector<HTMLElement>(`[data-row="a:${id}"]`)!;

async function open(path = '/approvals', params: Record<string, string> = {}, mock = '') {
  calls = installApi();
  if (mock) resetMock({ mock });
  approvals.reset();
  votes.reset();
  await session.init();
  router.navigate(path);
  render(Approvals, { params });
}

describe('Approvals inbox', () => {
  beforeEach(() => open());

  it('lists the requests for you first, one expanded, and the ones waiting for others below', async () => {
    const forYou = await screen.findByRole('heading', { name: 'For you' });
    expect(screen.getByRole('heading', { name: 'Waiting for others' })).toBeTruthy();
    expect(expanded()?.dataset.approval).toBe(DEPLOY);
    expect(within(expanded()!).getByRole('heading', { level: 3 }).textContent).toBe('Deploy acme/web to production?');
    const collapsed = [...document.querySelectorAll<HTMLButtonElement>('[data-row] button[aria-expanded="false"]')].map((b) => b.querySelector('span[title]')?.textContent);
    expect(collapsed).toContain('Delete notes/staging.md?');
    expect(collapsed).toContain('Review changes to Docs actions');
    // A command's row names the command, so two rows never both read "Run this command?".
    expect(collapsed).toContain('Run python3 sales_report.py --week 41?');
    expect(collapsed).not.toContain('Run this command?');
    expect(collapsed).toContain('Someone new messaged Bothy on Telegram');
    expect(collapsed).toContain('Review changes to the “Incident review” skill');
    expect(collapsed).toContain('Bothy was added to #support in Slack');
    expect(collapsed).toContain('Bothy was added to #general in Discord');
    // Slack and Discord decide who is in the team; only Telegram people wait here.
    expect(collapsed).not.toContain('Someone new messaged Bothy on Slack');
    expect(collapsed.at(-1)).toBe('Deploy acme/api to production?');
    expect(forYou).toBeTruthy();
    expect(screen.getByRole('link', { name: /Waiting/ }).getAttribute('aria-current')).toBe('page');
  });

  it("shows what changed in a skill since it was approved, file by file", async () => {
    await screen.findByRole('heading', { name: 'For you' });
    const toggle = [...document.querySelectorAll<HTMLButtonElement>('[data-row] button[aria-expanded="false"]')].find((b) => b.textContent?.includes('Incident review'))!;
    expect(toggle.textContent).toContain('2 files changed');
    await fireEvent.click(toggle);
    const card = await waitFor(() => {
      const a = document.querySelector<HTMLElement>('article[data-item="skill:incident-review"]');
      if (!a) throw new Error('not open');
      return a;
    });
    expect(within(card).getByText('scripts/post.sh')).toBeTruthy();
    expect(card.textContent).toContain('SKILL.md · changed');
    expect(card.textContent).toContain('scripts/post.sh · new');
    await fireEvent.click(within(card).getByRole('button', { name: 'Show what changed' }));
    expect(card.textContent).toContain('Post the review to #incidents when it is done.');
    await fireEvent.click(within(card).getByRole('button', { name: 'Show the file' }));
    expect(card.textContent).toContain('SLACK_WEBHOOK');
  });

  it('moves with J and K, opens with Enter and decides the open card with A', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.keyDown(window, { key: 'j' });
    expect(document.activeElement).toBe(row(SHELL).querySelector('button'));
    // The cursor is on a closed row: A must not decide the card that is open elsewhere.
    await fireEvent.keyDown(window, { key: 'a' });
    expect(posts()).toHaveLength(0);
    await fireEvent.click(document.activeElement as HTMLButtonElement);
    await waitFor(() => expect(document.activeElement?.closest('article')?.dataset.approval).toBe(SHELL));
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${SHELL}`);
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'allow_once' });
    await waitFor(() => expect(live()).toContain('Approved. Next: Send a message to “Ops team” on Telegram?'));
    await waitFor(() => expect(document.activeElement?.tagName).toBe('ARTICLE'));
    expect(expanded()?.textContent).toContain('Send a message to “Ops team” on Telegram?');
  });

  it('decides one request per key press, however long the key is held', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${DEPLOY}`);
    await fireEvent.keyDown(window, { key: 'a', repeat: true });
    await fireEvent.keyDown(window, { key: 'a' });
    await new Promise((r) => setTimeout(r, 20));
    expect(posts()).toHaveLength(1);
    await fireEvent.keyUp(window, { key: 'a' });
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(2));
  });

  it('opens the reason field with D and denies with Enter', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.keyDown(window, { key: 'd' });
    const field = await screen.findByLabelText(/Reason/);
    expect(document.activeElement).toBe(field);
    expect(posts()).toHaveLength(0);
    await fireEvent.submit(field.closest('form')!);
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(JSON.parse(posts()[0].body)).toEqual({ choice: 'deny' });
  });

  it('waits for the person to move before deciding a card that opened on its own', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    // The open request is decided from Slack; the next one opens in its place.
    approvals.upsert({ ...approvals.pending.find((a) => a.id === DEPLOY)!, status: 'allowed' });
    await waitFor(() => expect(expanded()?.dataset.approval).toBe(SHELL));
    await fireEvent.keyDown(window, { key: 'a' });
    expect(posts()).toHaveLength(0);
    await fireEvent.keyDown(window, { key: 'k' });
    await fireEvent.keyDown(window, { key: 'a' });
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url).toBe(`/api/approvals/${SHELL}`);
  });

  it('offers no shortcut for "Always allow"', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.click(row(SHELL).querySelector('button')!);
    const always = await within(expanded()!).findByRole('button', { name: /Always allow/ });
    expect(always.hasAttribute('aria-keyshortcuts')).toBe(false);
    const primary = within(expanded()!).getByRole('button', { name: 'Run command' });
    expect(primary.getAttribute('aria-keyshortcuts')).toBe('A');
  });

  it('decides review items through their own endpoint', async () => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.click(document.querySelector<HTMLElement>('[data-row="i:mcp:docs"] button')!);
    const card = await waitFor(() => document.querySelector<HTMLElement>('[data-item="mcp:docs"]')!);
    const select = within(card).getByLabelText('What this does') as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'external_send' } });
    await fireEvent.click(within(card).getByRole('button', { name: 'Approve changes' }));
    const approve = () => calls.filter((c) => c.method === 'POST' && c.url === '/api/mcp/docs/approve');
    await waitFor(() => expect(approve()).toHaveLength(1));
    expect(JSON.parse(approve()[0].body)).toEqual({ tools: { search_docs: { side_effect: 'external_send', private: false, hash: 'b3c1' } } });
    await waitFor(() => expect(document.querySelector('[data-row="i:mcp:docs"]')).toBeNull());
  });

  const openItem = async (id: string) => {
    await screen.findByRole('heading', { name: 'For you' });
    await fireEvent.click(document.querySelector<HTMLElement>(`[data-row="i:${id}"] button`)!);
    return waitFor(() => {
      const a = document.querySelector<HTMLElement>(`[data-item="${id}"]`);
      if (!a) throw new Error('not open');
      return a;
    });
  };

  it('lets in someone waiting on Telegram, with Let in on A and Block on D', async () => {
    const card = await openItem('sender:telegram:7215');
    expect(card.textContent).toContain('Jordan Lee (@jordan_l) wrote: “Hi, I run the Edinburgh office.');
    expect(within(card).queryByRole('button', { name: /guest|Link/ })).toBeNull();
    expect(within(card).getByRole('button', { name: 'Block' }).getAttribute('aria-keyshortcuts')).toBe('D');
    const letIn = within(card).getByRole('button', { name: 'Let in' });
    expect(letIn.getAttribute('aria-keyshortcuts')).toBe('A');
    await fireEvent.click(letIn);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/identities/telegram/7215' && JSON.parse(c.body).status === 'teammate')).toBe(true));
    await waitFor(() => expect(live()).toMatch(/^Let in\. Next: /));
  });

  it('turns on a new channel with what Bothy may do there, and says who sees its replies', async () => {
    const card = await openItem('group:spc_01jsupport0000000000000000');
    expect(card.textContent).toContain('Added by Sam.');
    expect(card.textContent).toContain('57 people are in this chat.');
    expect(card.textContent).toContain("People from other companies are in this channel. They'll see Bothy's replies, though Bothy won't answer them.");
    expect(card.textContent).not.toContain('Bothy answers only when mentioned.');
    const answer = within(card).getByRole('radio', { name: 'Answer questions' }) as HTMLInputElement;
    expect(answer.checked).toBe(true);
    await fireEvent.click(within(card).getByRole('radio', { name: 'Do work, with approval' }));
    await fireEvent.click(within(card).getByRole('button', { name: 'Turn on' }));
    const patch = () => calls.find((c) => c.method === 'PATCH' && c.url === '/api/spaces/spc_01jsupport0000000000000000');
    await waitFor(() => expect(patch()).toBeTruthy());
    expect(JSON.parse(patch()!.body)).toEqual({ enabled: true, tool_profile: 'standard' });
  });

  it('says turning on a Discord channel lets the whole server in, and Ignore leaves it off', async () => {
    const card = await openItem('group:spc_01jcommunity00000000000000');
    expect(card.textContent).toContain('Acme Community has 4,812 members.');
    expect(card.textContent).toContain('Turning this on lets all 4,812 members of Acme Community message Bothy, here and in direct messages.');
    expect(card.textContent).toContain('Bothy answers only when mentioned.');
    await fireEvent.click(within(card).getByRole('button', { name: 'Ignore' }));
    const patch = () => calls.find((c) => c.method === 'PATCH' && c.url === '/api/spaces/spc_01jcommunity00000000000000');
    await waitFor(() => expect(patch()).toBeTruthy());
    expect(JSON.parse(patch()!.body)).toEqual({ enabled: false });
  });

  it("names who asked on a chat app's request in its row", async () => {
    await screen.findByRole('heading', { name: 'For you' });
    expect(row('apr_01jteamchan000000000000000').textContent).toContain('Alex Kim · Slack');
  });
});

describe('Approvals inbox, other states', () => {
  // Where requests from chat apps now arrive; a notice, since nothing stops (§4.9).
  it('says above the list that people in chat apps no longer link to accounts, until it is reviewed', async () => {
    await open('/approvals', {}, 'team-access');
    expect(await screen.findByText(/^People in chat apps no longer link to Bothy accounts\. Check who can use Bothy and who can approve in People\./)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Open People' }));
    expect(router.path).toBe('/settings/people');
  });

  it('leaves the notice out without the reminder', async () => {
    await open();
    await screen.findByRole('heading', { name: 'For you' });
    expect(screen.queryByText(/no longer link to Bothy accounts/)).toBeNull();
  });
  it('opens /approvals/:id with that request expanded, pull request files listed and their diffs folded', async () => {
    await open(`/approvals/${PR}`, { id: PR });
    await waitFor(() => expect(expanded()?.dataset.approval).toBe(PR));
    expect(expanded()!.textContent).toContain('Open a pull request in acme/web?');
    // Each file stays listed while the diffs are folded, so the buttons stay in reach.
    expect(within(expanded()!).queryByRole('group', { name: 'Changes to src/auth/redirect.ts' })).toBeNull();
    expect(expanded()!.textContent).toMatch(/src\/auth\/redirect\.ts · \+3 −2/);
    await fireEvent.click(within(expanded()!).getByRole('button', { name: 'Show' }));
    expect(within(expanded()!).getByRole('group', { name: 'Changes to src/auth/redirect.ts' })).toBeTruthy();
    expect(expanded()!.textContent).toContain('This is the first pull request Bothy opens in acme/web for you.');
  });

  it('says a request was already decided, with its decided line', async () => {
    const done = recentApprovals[0];
    await open(`/approvals/${done.id}`, { id: done.id });
    expect(await screen.findByText('This request was already decided.')).toBeTruthy();
    await waitFor(() => expect(document.querySelector('[data-decided]')?.textContent).toContain('You approved deleting'));
    // This is the Approvals page: no button back to it.
    expect(screen.queryByRole('link', { name: 'Go to Approvals' })).toBeNull();
  });

  it('says who decided the open request when someone else decides it meanwhile', async () => {
    await open(`/approvals/${PR}`, { id: PR });
    await waitFor(() => expect(expanded()?.dataset.approval).toBe(PR));
    const pr = approvals.pending.find((a) => a.id === PR)!;
    const decided = { ...pr, status: 'denied', choice: 'deny', decided_by: 'usr_01jalex000000000000000000', decided_by_name: 'Alex Kim', decided_via: 'web', decided_at: new Date().toISOString(), comment: 'Two deploys already running' };
    calls = installApi({ override: (m, url) => (m === 'GET' && url === `/api/approvals/${PR}` ? decided : undefined) });
    FakeEventSource.last.emit('approval.resolved', { seq: 900, type: 'approval.resolved', at: new Date().toISOString(), data: { approval_id: PR, status: 'denied', choice: 'deny' } });
    expect(await screen.findByText('This request was already decided.')).toBeTruthy();
    expect(screen.queryByText('Nothing needs your approval.')).toBeNull();
  });

  it("says a request it can't find may not exist or not be visible", async () => {
    await open('/approvals/apr_nope', { id: 'apr_nope' });
    expect(await screen.findByText("This request doesn't exist, or you can't see it.")).toBeTruthy();
  });

  it('says when nothing waits', async () => {
    await open('/approvals', {}, 'approvals-empty');
    expect(await screen.findByText('Nothing needs your approval.')).toBeTruthy();
    expect(screen.getByText("When Bothy needs someone's OK, it shows up here.")).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'For you' })).toBeNull();
  });

  it('says when nothing waits with Bothy beside it, and nowhere else on the page', async () => {
    await open('/approvals', {}, 'approvals-empty');
    const empty = await screen.findByText('Nothing needs your approval.');
    expect(empty.closest('div.flex')!.querySelector('img[src="/icon.svg"]')!.getAttribute('alt')).toBe('');
    expect(document.querySelectorAll('img')).toHaveLength(1);
  });

  it('says who each request waiting for others waits on, with one status per row', async () => {
    await open('/approvals', {}, 'member');
    await screen.findByText('Nothing needs your approval.');
    const others = screen.getByRole('heading', { name: 'Waiting for others' }).closest('section')!;
    const rows = [...others.querySelectorAll<HTMLButtonElement>('[data-row] button[aria-expanded="false"]')];
    expect(rows.length).toBeGreaterThan(0);
    for (const r of rows) {
      const status = r.querySelector('span.shrink-0')!;
      expect(status.textContent).toMatch(/^(Waiting for .+|\d+ of \d+ approvals)$/);
      expect(status.querySelector('use')?.getAttribute('href')).toBe('#i-clock');
      expect(r.textContent).not.toContain('Waiting for you');
    }
  });

  it('counts only what waits for the viewer on the Waiting tab', async () => {
    await open('/approvals', {}, 'member');
    await screen.findByText('Nothing needs your approval.');
    expect(screen.getByRole('heading', { name: 'Waiting for others' })).toBeTruthy();
    // Above requests waiting for others, the page isn't empty.
    expect(screen.queryByText(/When Bothy needs someone's OK/)).toBeNull();
    expect(screen.getByRole('link', { name: /Waiting/ }).textContent).toBe('Waiting');
  });

  it("lists only action approvals when the inbox can't be read", async () => {
    await open('/approvals', {}, 'approvals-no-items');
    await screen.findByRole('heading', { name: 'For you' });
    expect(document.querySelector('[data-row^="i:"]')).toBeNull();
  });

  it("links to the chat a decision came from only when it is the viewer's own", async () => {
    const mine = recentApprovals.find((a) => a.session_id && a.principal_id)!;
    const theirs = { ...mine, id: 'apr_theirs', principal_id: 'usr_someone_else', session_id: 'ses_theirs' };
    calls = installApi({ override: (m, url) => (m === 'GET' && url.startsWith('/api/approvals?status=recent') ? { items: [mine, theirs] } : undefined) });
    approvals.reset();
    votes.reset();
    await session.init();
    session.me = { ...session.me!, user: { ...session.me!.user, id: mine.principal_id } };
    router.navigate('/approvals?tab=done');
    render(Approvals, { params: {} });
    await screen.findAllByText((_, el) => el?.hasAttribute('data-decided') ?? false);
    const hrefs = [...document.querySelectorAll('a')].map((a) => a.getAttribute('href'));
    expect(hrefs).toContain(`/chat/${mine.session_id}`);
    expect(hrefs).not.toContain('/chat/ses_theirs');
    // Without a link the meta line is just the time and Show, never an empty "· ·" between them.
    const metas = [...document.querySelectorAll('[data-decided]')].map((l) => l.nextElementSibling?.textContent?.replace(/\s+/g, ' ').trim());
    expect(metas).toHaveLength(2);
    expect(metas.filter((m) => /^\d{1,2}:\d{2}( [AP]M)? · Show$/.test(m ?? ''))).toHaveLength(1);
    expect(metas.some((m) => m?.includes('· ·'))).toBe(false);
  });

  // The server lists recent requests by when they were asked; a request
  // asked earlier and decided later used to sit under a newer decision.
  it('lists the Done tab by when each request was decided, newest first', async () => {
    const base = recentApprovals.find((a) => a.status === 'allowed')!;
    const t = (m: number) => new Date(Date.now() - m * 60_000).toISOString();
    const askedFirst = { ...base, id: 'apr_asked_first', created_at: t(30), decided_at: t(1), action: { ...base.action!, payload: { path: 'notes/first.md' }, paths: ['notes/first.md'] } };
    const askedLater = { ...base, id: 'apr_asked_later', created_at: t(20), decided_at: t(2), action: { ...base.action!, payload: { path: 'notes/later.md' }, paths: ['notes/later.md'] } };
    calls = installApi({ override: (m, url) => (m === 'GET' && url.startsWith('/api/approvals?status=recent') ? { items: [askedLater, askedFirst] } : undefined) });
    approvals.reset();
    votes.reset();
    await session.init();
    router.navigate('/approvals?tab=done');
    render(Approvals, { params: {} });
    await screen.findAllByText((_, el) => el?.hasAttribute('data-decided') ?? false);
    const lines = [...document.querySelectorAll('[data-decided]')].map((l) => l.textContent ?? '');
    expect(lines.findIndex((l) => l.includes('first.md'))).toBeLessThan(lines.findIndex((l) => l.includes('later.md')));
  });

  it('groups the Done tab by day, every line naming what was decided', async () => {
    await open('/approvals?tab=done');
    expect(await screen.findByRole('heading', { name: 'Today' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Yesterday' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Earlier' })).toBeTruthy();
    const lines = [...document.querySelectorAll('[data-decided]')].map((l) => l.textContent?.replace(/\s+/g, ' ').trim());
    expect(lines.some((l) => l?.startsWith('You approved deleting notes/old-draft.md'))).toBe(true);
    expect(lines.some((l) => l?.startsWith('You denied running rm -rf build/ · “Not while the build server is in use”'))).toBe(true);
    expect(lines.some((l) => l?.startsWith("Expired: Bothy didn't send data to api.example.com"))).toBe(true);
    // The decided sentence is the page's main text; its time, source and Show sit on one muted line under it.
    const first = document.querySelector('[data-decided]')!;
    expect(first.className).toMatch(/text-body/);
    expect(first.className).toMatch(/text-text/);
    expect(first.nextElementSibling?.textContent?.replace(/\s+/g, ' ').trim()).toMatch(/^\d{1,2}:\d{2}( [AP]M)? · .+ · Show$/);
    expect(screen.getByRole('link', { name: 'Done' }).getAttribute('aria-current')).toBe('page');
  });
});

describe('Approvals inbox, a decision racing its event', () => {
  // The event stream can drop a decided row before the decision's own reply
  // lands; the card then folds away (inert) and must not read its state.
  it("doesn't read a card's state once its row is folding away", async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const realAnimate = Element.prototype.animate;
    // An animation that never ends keeps the folding row inert.
    Element.prototype.animate = (() => ({ finished: new Promise(() => {}), cancel() {}, play() {}, pause() {}, onfinish: null, currentTime: 0 })) as never;
    try {
      calls = installApi({
        override: (m, url) => {
          if (m !== 'POST' || url !== `/api/approvals/${SHELL}`) return undefined;
          const a = approvals.pending.find((x) => x.id === SHELL)!;
          flushSync(() => (approvals.pending = approvals.pending.filter((x) => x.id !== SHELL)));
          return { ...a, status: 'allowed', choice: 'allow_once', decided_by: session.me?.user.id, decided_at: new Date().toISOString() };
        },
      });
      approvals.reset();
      votes.reset();
      await session.init();
      router.navigate('/approvals');
      render(Approvals, { params: {} });
      await screen.findByRole('heading', { name: 'For you' });
      await fireEvent.click(row(SHELL).querySelector('button')!);
      await waitFor(() => expect(expanded()?.dataset.approval).toBe(SHELL));
      await fireEvent.keyDown(window, { key: 'a' });
      await waitFor(() => expect(posts()).toHaveLength(1));
      await waitFor(() => expect(live()).toContain('Approved.'));
      expect(warn.mock.calls.map((c) => c.join(' ')).filter((c) => c.includes('derived_inert'))).toEqual([]);
    } finally {
      Element.prototype.animate = realAnimate;
      warn.mockRestore();
    }
  });
});
