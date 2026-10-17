import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { sessions as fixtureSessions } from '../../../mock/fixtures';
import App from '../../App.svelte';
import { router } from '../../lib/router.svelte';
import { approvals, session } from '../../lib/stores/session.svelte';
import { shell, theme } from '../../lib/stores/ui.svelte';
import { installApi, type Call } from '../../test/api';
import { chats } from './chats.svelte';

const nav = () => screen.getByRole('navigation', { name: 'Main' });

// installApi hands its options to resetMock, which takes a scenario list as `mock`.
const withMock = (opts: { mock?: string; override?: (m: string, url: string) => unknown }) => installApi(opts as Parameters<typeof installApi>[0]);

async function start(path: string, opts: { mock?: string; override?: (m: string, url: string) => unknown } = {}): Promise<Call[]> {
  const calls = withMock(opts);
  session.ready = false;
  session.me = null;
  await session.init();
  router.navigate(path);
  render(App);
  await waitFor(() => expect(nav()).toBeTruthy());
  return calls;
}

afterEach(() => {
  shell.search = shell.shortcuts = shell.pause = shell.drawer = false;
  chats.reset();
});

describe('banners (§4.9)', () => {
  it('leaves the spending limit to Activity → Spending, which says it under its meter', async () => {
    await start('/activity', { mock: 'spend-limit' });
    const banner = /^Bothy reached today's .+ spending limit\./;
    await waitFor(() => expect(screen.getByText(banner)).toBeTruthy());
    router.navigate('/activity/spending');
    await waitFor(() => expect(screen.queryByText(banner)).toBeNull());
  });
});

describe('sidebar (§4.2)', () => {
  it('has New chat, Search, the three destinations and the chats', async () => {
    await start('/activity', { mock: 'team-chats' });
    const n = within(nav());
    expect(n.getByRole('link', { name: 'Bothy' }).getAttribute('href')).toBe('/chat');
    expect(n.getByRole('link', { name: 'New chat' })).toBeTruthy();
    expect(n.getByRole('button', { name: /Search/ })).toBeTruthy();
    expect(n.getByRole('link', { name: 'Activity' }).getAttribute('aria-current')).toBe('page');
    // The count is what GET /api/inbox says the viewer can decide now.
    const inbox = (await (await fetch('/api/inbox')).json()) as { items: { for_you: boolean }[] };
    const mine = inbox.items.filter((i) => i.for_you).length;
    expect(mine).toBeGreaterThan(0);
    await waitFor(() => expect(n.getByRole('link', { name: new RegExp(`^Approvals\\s*${mine}\\s*waiting$`) })).toBeTruthy());
    await waitFor(() => expect(n.getByRole('link', { name: 'Fix login redirect' })).toBeTruthy());
    for (const label of ['Today', 'Previous 7 days', 'Older']) expect(n.getByRole('list', { name: label })).toBeTruthy();
    // Chats in chat apps belong to the people and channels there, not to anyone's sidebar.
    expect(n.queryByRole('list', { name: 'Group chats' })).toBeNull();
  });

  it('marks the chat Bothy works in and the one waiting for approval', async () => {
    await start('/activity', { mock: 'team-chats' });
    const n = within(nav());
    await waitFor(() => expect(n.getByText('Bothy is working')).toBeTruthy());
    expect(n.getByText('Bothy is working').closest('li')?.textContent).toContain('Fix login redirect');
    await waitFor(() => expect(n.getByText('Waiting for approval').closest('li')?.textContent).toContain('v2.3 launch checklist'));
  });

  it('keeps the trailing slot only for a chat with a state, so other titles use the full width', async () => {
    await start('/activity', { mock: 'team-chats' });
    const n = within(nav());
    await waitFor(() => expect(n.getByText('Waiting for approval')).toBeTruthy());
    const row = (title: string) => n.getByRole('link', { name: title }).closest('li')!;
    // link + ⋯ only, against link + glyph + ⋯
    expect(row('Q3 pricing notes').children).toHaveLength(2);
    expect(row('v2.3 launch checklist').children).toHaveLength(3);
  });

  it('keeps a row highlighted while its menu is open', async () => {
    await start('/activity', { mock: 'team-chats' });
    const n = within(nav());
    const trigger = await waitFor(() => n.getByRole('button', { name: 'Options for Q3 pricing notes' }));
    await fireEvent.click(trigger);
    await waitFor(() => expect(screen.getByRole('menu')).toBeTruthy());
    expect(trigger.closest('li')?.classList.contains('bg-hover')).toBe(true);
  });

  it('draws a hairline above the chats only once they have scrolled', async () => {
    await start('/activity', { mock: 'long-chats' });
    await waitFor(() => expect(within(nav()).getByRole('button', { name: 'Show more' })).toBeTruthy());
    const list = within(nav()).getByRole('list', { name: 'Today' }).parentElement!;
    expect(list.classList.contains('border-transparent')).toBe(true);
    list.scrollTop = 120;
    await fireEvent.scroll(list);
    expect(list.classList.contains('border-border')).toBe(true);
  });

  it('never lists a chat without messages, and titles an untitled one by its first words', async () => {
    await start('/activity', { mock: 'team-chats' });
    const n = within(nav());
    await waitFor(() => expect(n.getByRole('link', { name: 'Draft the October release notes from the merged…' })).toBeTruthy());
    expect(n.getAllByRole('link').filter((a) => a.getAttribute('href')?.startsWith('/chat/')).map((a) => a.textContent?.trim())).not.toContain('');
    expect(nav().querySelector('a[href="/chat/ses_01jteamempty0000000000000000"]')).toBeNull();
  });

  it('shows Show more after 30 chats', async () => {
    await start('/activity', { mock: 'long-chats' });
    const n = within(nav());
    await waitFor(() => expect(n.getByRole('button', { name: 'Show more' })).toBeTruthy());
    const count = () => nav().querySelectorAll('a[href^="/chat/"]').length;
    expect(count()).toBe(30);
    await fireEvent.click(n.getByRole('button', { name: 'Show more' }));
    expect(count()).toBe(44);
    expect(n.queryByRole('button', { name: 'Show more' })).toBeNull();
  });

  it('gives admins Settings with the attention dot, and members no Settings at all', async () => {
    await start('/activity');
    await waitFor(() => expect(within(nav()).getByRole('link', { name: /^Settings/ })).toBeTruthy());
    await waitFor(() => expect(within(nav()).getByRole('link', { name: 'Settings Needs attention' })).toBeTruthy());
  });

  it('has no Settings item for members', async () => {
    await start('/activity', { mock: 'member' });
    await waitFor(() => expect(within(nav()).getByRole('button', { name: /^Account:/ })).toBeTruthy());
    expect(within(nav()).queryByRole('link', { name: /Settings/ })).toBeNull();
  });
});

describe('focus (§16)', () => {
  it('leaves focus alone on the first load, then moves it to the new page’s h1', async () => {
    await start('/activity');
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Activity' })).toBeTruthy());
    expect(document.activeElement).toBe(document.body);
    await fireEvent.click(within(nav()).getByRole('link', { name: 'Knowledge' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: 'Knowledge' })));
  });

  it('starts with the skip link', async () => {
    await start('/activity');
    const first = document.querySelector('a[href], button');
    expect(first?.textContent).toBe('Skip to content');
    expect(first?.getAttribute('href')).toBe('#main');
    expect(screen.getByRole('main').id).toBe('main');
  });
});

describe('settings mode (§4.3)', () => {
  it('swaps the sidebar for the settings groups, with dots on Security and Connections', async () => {
    await start('/activity', { mock: 'team-chats' });
    router.navigate('/settings/connections/github');
    const n = within(nav());
    await waitFor(() => expect(n.getByRole('link', { name: 'Back to Bothy' }).getAttribute('href')).toBe('/activity'));
    expect(n.getByRole('list', { name: 'You' })).toBeTruthy();
    expect(n.getAllByRole('link').map((a) => a.textContent?.replace(/\s+/g, ' ').trim())).toEqual(['Back to Bothy', 'Account', 'General', 'Models', 'Connections Needs attention', 'People', 'Security Needs attention', 'Advanced']);
    expect(n.getByRole('link', { name: /^Connections/ }).getAttribute('aria-current')).toBe('page');
    expect(n.queryByRole('link', { name: 'New chat' })).toBeNull();
  });

  it('shows members only Account', async () => {
    await start('/settings/account', { mock: 'member' });
    const n = within(nav());
    await waitFor(() => expect(n.getByRole('link', { name: 'Account' })).toBeTruthy());
    expect(n.queryByRole('list', { name: 'Workspace' })).toBeNull();
  });
});

describe('account menu', () => {
  it('lists the items, with Theme as a submenu of menuitemradio choices', async () => {
    await start('/activity');
    const trigger = await waitFor(() => within(nav()).getByRole('button', { name: /^Account:/ }));
    await fireEvent.click(trigger);
    const menu = screen.getByRole('menu');
    expect(within(menu).getAllByRole('menuitem').map((i) => i.textContent?.trim())).toEqual(['Your account', 'Theme', 'Keyboard shortcuts', 'Pause Bothy…', 'Sign out']);
    expect(document.activeElement?.textContent?.trim()).toBe('Your account');
    await fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent?.trim()).toBe('Theme');
    await fireEvent.keyDown(menu, { key: 'ArrowRight' });
    const sub = await waitFor(() => screen.getByRole('menu', { name: 'Theme' }));
    const radios = within(sub).getAllByRole('menuitemradio');
    expect(radios.map((r) => r.textContent?.trim())).toEqual(['System', 'Light', 'Dark']);
    expect(radios[0].getAttribute('aria-checked')).toBe('true');
    await fireEvent.keyDown(sub, { key: 'ArrowLeft' });
    expect(screen.queryByRole('menu', { name: 'Theme' })).toBeNull();
    expect(document.activeElement?.textContent?.trim()).toBe('Theme');
    await fireEvent.keyDown(menu, { key: 'Enter' });
    await fireEvent.click(document.activeElement as HTMLElement);
    await fireEvent.click(within(screen.getByRole('menu', { name: 'Theme' })).getByRole('menuitemradio', { name: 'Dark' }));
    expect(theme.pref).toBe('dark');
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
    theme.set('system');
  });

  it('closes on Escape and gives focus back', async () => {
    await start('/activity');
    const trigger = await waitFor(() => within(nav()).getByRole('button', { name: /^Account:/ }));
    await fireEvent.click(trigger);
    await fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('has no Pause for members', async () => {
    await start('/activity', { mock: 'member' });
    await fireEvent.click(await waitFor(() => within(nav()).getByRole('button', { name: /^Account:/ })));
    expect(within(screen.getByRole('menu')).queryByRole('menuitem', { name: /Pause/ })).toBeNull();
  });

  it('pauses Bothy after a confirmation that starts on Cancel', async () => {
    const calls = await start('/activity');
    await fireEvent.click(await waitFor(() => within(nav()).getByRole('button', { name: /^Account:/ })));
    await fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Pause Bothy…' }));
    const dialog = await waitFor(() => screen.getByRole('dialog', { name: 'Pause Bothy?' }));
    expect(dialog.textContent).toContain('Everything running stops now, and nothing new starts, including schedules, until someone resumes it. Approvals that are waiting will be cancelled.');
    expect(document.activeElement?.textContent?.trim()).toBe('Cancel');
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Pause Bothy' }));
    await waitFor(() => expect(session.paused).toBe(true));
    expect(calls.some((c) => c.url === '/api/admin/pause' && c.body === '{"paused":true}')).toBe(true);
    await waitFor(() => expect(screen.getByText('Bothy is paused. Nothing new starts until an admin resumes it.')).toBeTruthy());
  });
});

describe('shortcuts (§6.4)', () => {
  it('opens search with Ctrl K and "/", and the shortcuts with "?"', async () => {
    await start('/activity', { mock: 'team-chats' });
    await fireEvent.keyDown(window, { key: '/' });
    await waitFor(() => expect(screen.getByRole('dialog', { name: 'Search' })).toBeTruthy());
    expect(document.activeElement?.getAttribute('role')).toBe('combobox');
    await fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Escape' });
    await waitFor(() => expect(shell.search).toBe(false));

    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    expect(shell.search).toBe(true);
    shell.search = false;
    await waitFor(() => expect(document.querySelector('dialog[open]')).toBeNull());

    await fireEvent.keyDown(window, { key: '?', shiftKey: true });
    await waitFor(() => expect(screen.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeTruthy());
    expect(screen.getByText('Stop Bothy (in a running chat) or close')).toBeTruthy();
    // Nothing in it takes input, so focus starts on the list, not on Close.
    await waitFor(() => expect(document.activeElement?.contains(screen.getByText('Approve'))).toBe(true));
  });

  it('starts a new chat with Ctrl Shift O', async () => {
    await start('/activity');
    await fireEvent.keyDown(window, { key: 'O', ctrlKey: true, shiftKey: true });
    expect(router.path).toBe('/chat');
  });

  it('ignores single keys while someone types', async () => {
    await start('/activity');
    const input = document.createElement('input');
    document.body.append(input);
    input.focus();
    await fireEvent.keyDown(input, { key: '/' });
    await fireEvent.keyDown(input, { key: '?' });
    expect(shell.search || shell.shortcuts).toBe(false);
    input.remove();
  });
});

describe('search dialog (§6.3)', () => {
  it('lists recent chats, then results with the matches marked, and opens one', async () => {
    await start('/activity', { mock: 'team-chats' });
    await waitFor(() => expect(within(nav()).getByRole('link', { name: 'Fix login redirect' })).toBeTruthy());
    shell.search = true;
    const box = await waitFor(() => screen.getByRole('combobox', { name: 'Search chats' }));
    const list = screen.getByRole('listbox');
    expect(list.getAttribute('aria-label')).toBe('Recent chats');
    expect(within(list).getAllByRole('option')).toHaveLength(5);
    await fireEvent.input(box, { target: { value: 'launch' } });
    await waitFor(() => expect(within(screen.getByRole('listbox', { name: 'Chats' })).getAllByRole('option').length).toBeGreaterThan(0));
    const first = within(screen.getByRole('listbox')).getAllByRole('option')[0];
    expect(first.textContent).toContain('v2.3 launch checklist');
    expect(first.querySelector('mark')?.textContent).toBe('launch');
    expect(box.getAttribute('aria-activedescendant')).toBe(first.id);
    await fireEvent.keyDown(box, { key: 'Enter' });
    await waitFor(() => expect(router.path).toBe('/chat/ses_01jteamlaunch000000000000000'));
    expect(shell.search).toBe(false);
  });

  it('says when nothing matches', async () => {
    await start('/activity', { mock: 'team-chats' });
    shell.search = true;
    const box = await waitFor(() => screen.getByRole('combobox'));
    await fireEvent.input(box, { target: { value: 'zebra' } });
    await waitFor(() => expect(screen.getByText('No results for “zebra”.')).toBeTruthy());
  });
});

describe('chat row menu', () => {
  it('renames a chat', async () => {
    const calls = await start('/activity', { mock: 'team-chats' });
    await fireEvent.click(await waitFor(() => within(nav()).getByRole('button', { name: 'Options for Q3 pricing notes' })));
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Rename' }));
    const dialog = await waitFor(() => screen.getByRole('dialog', { name: 'Rename chat' }));
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Q3 pricing decisions' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(within(nav()).getByRole('link', { name: 'Q3 pricing decisions' })).toBeTruthy());
    expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/sessions/ses_01jteampricing00000000000000')).toBe(true);
  });

  it('deletes a chat after a confirmation that starts on Cancel, and leaves it if it was open', async () => {
    installApi();
    await session.init();
    router.navigate(`/chat/${fixtureSessions[3].id}`);
    render(App);
    const trigger = await waitFor(() => within(nav()).getByRole('button', { name: `Options for ${fixtureSessions[3].title}` }));
    await fireEvent.click(trigger);
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Delete chat…' }));
    const dialog = await waitFor(() => screen.getByRole('dialog', { name: `Delete “${fixtureSessions[3].title}”?` }));
    expect(document.activeElement?.textContent?.trim()).toBe('Cancel');
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete chat' }));
    await waitFor(() => expect(router.path).toBe('/chat'));
    expect(within(nav()).queryByRole('link', { name: fixtureSessions[3].title })).toBeNull();
  });
});

describe('banners and blocking states', () => {
  it('shows the unlock screen to admins when the keys are locked, and the banner in Settings', async () => {
    await start('/activity', { mock: 'sealed' });
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Unlock Bothy' })).toBeTruthy());
    expect(screen.queryByText("Bothy's saved keys are locked, so it can't reach its models or connections.")).toBeNull();
    router.navigate('/settings/connections/github');
    await waitFor(() => expect(screen.getByText("Bothy's saved keys are locked, so it can't reach its models or connections.")).toBeTruthy());
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Unlock Bothy' })).toBeTruthy());
    await fireEvent.input(screen.getByLabelText('Passphrase'), { target: { value: 'correct horse' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    // Settings is its own chunk, loaded only after the unlock: slow on a busy CI runner.
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'GitHub' })).toBeTruthy(), { timeout: 5000 });
  });

  it('lets a dismissible banner go for the rest of the session', async () => {
    await start('/activity', { mock: 'restart' });
    await waitFor(() => expect(screen.getByText('Restart Bothy to apply your changes.')).toBeTruthy());
    await fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(screen.queryByText('Restart Bothy to apply your changes.')).toBeNull();
    expect(JSON.parse(sessionStorage.getItem('bothy.dismissed') ?? '[]')).toContain('restart_required');
    session.dismissed = [];
    sessionStorage.clear();
  });

  it('says a page does not exist', async () => {
    await start('/nope');
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy());
    expect(screen.getByText("This page doesn't exist.")).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Go to chat' }).getAttribute('href')).toBe('/chat');
  });

  it('tells a member an admin page is for admins', async () => {
    for (const [path, title] of [
      ['/activity/audit', 'Audit log'],
      ['/settings/security', 'Security'],
      ['/settings/general/personality', 'General'],
    ]) {
      await start(path, { mock: 'member' });
      await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: title })).toBeTruthy());
      expect(screen.getByText('Only admins can see this page.')).toBeTruthy();
      document.body.innerHTML = '';
    }
  });

  it('treats an unknown settings group as a missing page', async () => {
    await start('/settings/nope');
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy());
  });
});

describe('phones', () => {
  const narrow = () => {
    window.matchMedia = ((q: string) => ({ matches: q.includes('max-width') || q.includes('coarse'), media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
  };
  afterEach(() => {
    delete (window as { matchMedia?: unknown }).matchMedia;
  });

  it('lists the settings groups at /settings instead of opening General', async () => {
    narrow();
    await start('/settings');
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy());
    expect(router.path).toBe('/settings');
    const main = within(screen.getByRole('main'));
    expect(main.getAllByRole('link').map((a) => a.getAttribute('href')).filter((h) => h?.startsWith('/settings'))).toEqual(['/settings/account', '/settings/general', '/settings/models', '/settings/connections', '/settings/people', '/settings/security', '/settings/advanced']);
  });

  it('opens the sidebar as a drawer from the top bar and says how many approvals wait', async () => {
    await start('/activity');
    // The count follows the approvals fixtures, so it is read rather than fixed.
    const menu = await waitFor(() => screen.getByRole('button', { name: /^Open menu, \d+ approvals waiting$/ }));
    expect(approvals.waiting).toBeGreaterThan(1);
    expect(menu.getAttribute('aria-label')).toBe(`Open menu, ${approvals.waiting} approvals waiting`);
    expect(screen.getByRole('link', { name: `${approvals.waiting} waiting` }).getAttribute('href')).toBe('/approvals');
    await fireEvent.click(menu);
    const drawer = await waitFor(() => screen.getByRole('dialog', { name: 'Menu' }));
    expect(within(drawer).getByRole('link', { name: 'New chat' })).toBeTruthy();
    await fireEvent.click(within(drawer).getByRole('link', { name: 'Knowledge' }));
    await waitFor(() => expect(shell.drawer).toBe(false));
    expect(router.path).toBe('/knowledge');
  });

  it('has a close button in the drawer in Settings too', async () => {
    await start('/settings/general');
    await fireEvent.click(await waitFor(() => screen.getByRole('button', { name: /^Open menu/ })));
    const drawer = await waitFor(() => screen.getByRole('dialog', { name: 'Menu' }));
    expect(within(drawer).getByRole('link', { name: 'Back to Bothy' })).toBeTruthy();
    await fireEvent.click(within(drawer).getByRole('button', { name: 'Close menu' }));
    await waitFor(() => expect(shell.drawer).toBe(false));
  });

  it('drops the waiting button when nothing waits', async () => {
    await start('/activity', { mock: 'approvals-empty' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Open menu' })).toBeTruthy());
    expect(screen.queryByRole('link', { name: /waiting/ })).toBeNull();
  });
});

describe('member view of approvals', () => {
  it('counts only what names the member', async () => {
    await start('/activity', { mock: 'member' });
    await waitFor(() => expect(within(nav()).getByRole('link', { name: 'Approvals' })).toBeTruthy());
  });
});
