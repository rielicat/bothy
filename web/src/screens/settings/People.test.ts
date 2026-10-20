import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { setCsrf } from '../../lib/api/client';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
import { FakeEventSource } from '../../test/setup';
import Settings from './Settings.svelte';
import { settingsConfig } from './store.svelte';

async function open(mock = '') {
  const calls = installApi();
  if (mock) resetMock({ mock });
  session.ready = false;
  await session.init();
  setCsrf('mock-csrf');
  settingsConfig.view = null;
  render(Settings, { params: { group: 'people', page: 'people' } });
  return calls;
}

const menu = async (name: string) => {
  await fireEvent.click(await screen.findByRole('button', { name: `Options for ${name}` }));
  return screen.findByRole('menu');
};

describe('Settings → People', () => {
  it('lists people by name with their role, and nobody can change the owner or themselves', async () => {
    await open();
    expect(await screen.findByText('Jordan Kim')).toBeTruthy();
    expect(screen.getAllByText("Member · hasn't signed in yet").length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: 'Options for Sam' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Options for Priya Rao' })).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/usr_/);
  });

  it('adds a person with a one-time password to send them', async () => {
    const calls = await open();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add person' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add person' });
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Maya Chen' } });
    await fireEvent.input(within(dialog).getByLabelText('Username'), { target: { value: 'maya' } });
    await fireEvent.click(within(dialog).getByRole('radio', { name: 'Admin' }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('Maya Chen can sign in now. Send them these details:')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Copy' })));
    const post = JSON.parse(calls.find((c) => c.method === 'POST' && c.url === '/api/users')!.body);
    expect(post).toMatchObject({ display_name: 'Maya Chen', username: 'maya', role: 'admin' });
    expect(post.password).toBeUndefined();
    expect(within(dialog).getByText(/One-time password: h7Fq-2mKx-9wPr-tB4n/)).toBeTruthy();
    expect(within(dialog).getByText("They'll choose their own password when they first sign in.")).toBeTruthy();
    // Single-user installs switch to team mode first, silently (D19).
    expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/config' && JSON.parse(c.body).set.mode === 'multi')).toBe(true);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(await screen.findByText('Maya Chen')).toBeTruthy();
  });

  it('suggests the username from the name', async () => {
    await open();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add person' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add person' });
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Riley Park' } });
    expect((within(dialog).getByLabelText('Username') as HTMLInputElement).value).toBe('riley');
  });

  it('checks the username and says when it is taken', async () => {
    await open();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add person' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add person' });
    await fireEvent.input(within(dialog).getByLabelText('Username'), { target: { value: '-x' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('Enter their name.')).toBeTruthy();
    expect(within(dialog).getByText(/^Use up to 32 letters/)).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Alex Morgan' } });
    await fireEvent.input(within(dialog).getByLabelText('Username'), { target: { value: 'alex' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('That username is taken. Try another.')).toBeTruthy();
  });

  it('changes a role from the menu', async () => {
    const calls = await open();
    const m = await menu('Alex');
    expect(within(m).getByRole('menuitemradio', { name: 'Member' }).getAttribute('aria-checked')).toBe('true');
    await fireEvent.click(within(m).getByRole('menuitemradio', { name: 'Admin' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/users/usr_01jalex000000000000000000' && JSON.parse(c.body).role === 'admin')).toBe(true));
  });

  it('sets and clears a daily limit by username, in the same dialog as people in chat apps', async () => {
    const calls = await open();
    let m = await menu('Alex');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Daily limit…' }));
    let dialog = await screen.findByRole('dialog', { name: 'Daily limit for Alex' });
    // "Use the default" says what the default is, instead of a "No personal limit" box that meant the same.
    expect((within(dialog).getByRole('radio', { name: 'Use the default ($2.00 a day)' }) as HTMLInputElement).checked).toBe(true);
    await fireEvent.click(within(dialog).getByRole('radio', { name: 'Set a limit' }));
    await fireEvent.input(within(dialog).getByLabelText('Daily limit'), { target: { value: '3' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body).set)).toContainEqual({ 'budgets.per_user_daily_usd.alex': 3 }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Daily limit for Alex' })).toBeNull());

    m = await menu('Alex');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Daily limit…' }));
    dialog = await screen.findByRole('dialog', { name: 'Daily limit for Alex' });
    expect((within(dialog).getByLabelText('Daily limit') as HTMLInputElement).value).toBe('3.00');
    await fireEvent.click(within(dialog).getByRole('radio', { name: /^Use the default/ }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body).set)).toContainEqual({ 'budgets.per_user_daily_usd.alex': null }));
  });

  it('turns an account off only after the confirmation, and back on', async () => {
    const calls = await open();
    let m = await menu('Priya Rao');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Turn off account…' }));
    const dialog = await screen.findByRole('dialog', { name: "Turn off Priya Rao's account?" });
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Turn off account' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/users/usr_01jpriya00000000000000000')).toBe(true));
    expect(await screen.findByText('Admin · Turned off', { selector: 'p' })).toBeTruthy();
    m = await menu('Priya Rao');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Turn on account' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && JSON.parse(c.body).disabled === false)).toBe(true));
  });

  it('resets a password to a new one-time password', async () => {
    await open();
    const m = await menu('Jordan Kim');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Reset password…' }));
    const dialog = await screen.findByRole('dialog', { name: "Reset Jordan Kim's password?" });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Reset password' }));
    expect(await within(dialog).findByText(/One-time password: Wd4k-7RmP-q9Xz-3hTn/)).toBeTruthy();
    await waitFor(() => expect(screen.getByRole('dialog', { name: 'New password for Jordan Kim' })).toBe(dialog));
  });

  it("says why a reset was refused, and offers none for a turned-off account", async () => {
    const refused = new Response(JSON.stringify({ error: { code: 'forbidden', message: "only the owner can reset an admin's password" } }), { status: 403 });
    installApi({ override: (m, url) => (m === 'POST' && url.endsWith('/password') ? refused : undefined) });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    render(Settings, { params: { group: 'people', page: 'people' } });
    let m = await menu('Priya Rao');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Reset password…' }));
    const dialog = await screen.findByRole('dialog', { name: "Reset Priya Rao's password?" });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Reset password' }));
    expect(await within(dialog).findByText("Only the owner can reset an admin's password.")).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    m = await menu('Priya Rao');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Turn off account…' }));
    await fireEvent.click(within(await screen.findByRole('dialog', { name: "Turn off Priya Rao's account?" })).getByRole('button', { name: 'Turn off account' }));
    await screen.findByText('Admin · Turned off', { selector: 'p' });
    m = await menu('Priya Rao');
    expect(within(m).queryByRole('menuitem', { name: 'Reset password…' })).toBeNull();
    expect(within(m).getByRole('menuitem', { name: 'Turn on account' })).toBeTruthy();
  });

  it("leaves another admin's account to the owner", async () => {
    await open();
    session.me = { ...session.me!, user: { ...session.me!.user, role: 'admin' } };
    const m = await menu('Priya Rao');
    expect(within(m).getAllByRole('menuitem').map((i) => i.textContent?.trim())).toEqual(['Daily limit…']);
    expect(within(m).queryAllByRole('menuitemradio')).toEqual([]);
    session.me = { ...session.me!, user: { ...session.me!.user, role: 'owner' } };
  });

  it('says on the field when the server refuses a name', async () => {
    const refused = new Response(JSON.stringify({ error: { code: 'bad_request', message: 'display name must be at most 64 printable characters' } }), { status: 400 });
    installApi({ override: (m, url) => (m === 'POST' && url === '/api/users' ? refused : undefined) });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    render(Settings, { params: { group: 'people', page: 'people' } });
    await fireEvent.click(await screen.findByRole('button', { name: 'Add person' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add person' });
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Maya\u200bChen' } });
    await fireEvent.input(within(dialog).getByLabelText('Username'), { target: { value: 'maya' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('Use at most 64 characters, with no invisible ones.')).toBeTruthy();
    expect(within(dialog).queryByText("Bothy couldn't add them. Try again.")).toBeNull();
  });

  it("opens Add person when setup's \"Add people\" sends someone here", async () => {
    router.navigate('/settings/people?add=1');
    await open();
    expect(await screen.findByRole('dialog', { name: 'Add person' })).toBeTruthy();
    expect(router.search).toBe('');
  });

  it('invites the owner to add people when there is nobody else', async () => {
    await open('people-solo,chat-people-none');
    expect(await screen.findByText("It's just you so far. Add people so they get their own sign-in.")).toBeTruthy();
    expect(screen.getAllByRole('button', { name: 'Add person' })).toHaveLength(1);
  });

  it("doesn't call the owner alone while people use Bothy in chat apps", async () => {
    await open('people-solo');
    expect(await screen.findByText('Only you sign in to Bothy on the web. Add people so they get their own sign-in.')).toBeTruthy();
    expect(screen.queryByText(/^It's just you so far/)).toBeNull();
    const s = within((await screen.findByRole('heading', { name: 'In chat apps' })).closest('section')!);
    expect(await s.findByText('Sam', { selector: 'p' })).toBeTruthy();
  });
});

describe('Settings → People → In chat apps', () => {
  const section = async () => within((await screen.findByRole('heading', { name: 'In chat apps' })).closest('section')!);
  const rowOf = (s: Awaited<ReturnType<typeof section>>, name: string) => s.getByText(name, { selector: 'p' }).closest('li')!;

  // The order is the server's (approvers, then last active, then blocked); the list keeps it as given.
  it("lists people in the server's order, with their app, spending today and whether they can approve", async () => {
    await open();
    const s = await section();
    await s.findByText('Sam', { selector: 'p' });
    expect(s.getByText("People who can approve get approval requests in their direct messages. Changes to Bothy's settings still need an admin here.")).toBeTruthy();
    const names = [...s.getAllByRole('listitem')].map((li) => li.querySelector('p')?.textContent);
    expect(names).toEqual(['Sam', 'Alex Kim', 'Morgan Reyes', 'Riley Park', 'Casey']);
    expect(rowOf(s, 'Alex Kim').textContent).toContain('@alexk · Slack');
    expect(rowOf(s, 'Alex Kim').textContent).toMatch(/Active .+ · \$1\.15 of \$2\.00 today/);
    expect(rowOf(s, 'Riley Park').textContent).toContain('@riley · Slack · No longer in Acme');
    expect(rowOf(s, 'Casey').textContent).toContain('Telegram · Blocked');
    expect(within(rowOf(s, 'Casey')).queryByRole('switch')).toBeNull();
    const riley = within(rowOf(s, 'Riley Park')).getByRole('switch', { name: 'Can approve: Riley Park' });
    expect(riley.getAttribute('aria-disabled')).toBe('true');
    // The row's own meta line says why, so no second line repeats it.
    expect(document.getElementById(riley.getAttribute('aria-describedby')!)?.textContent).toContain('No longer in Acme');
    expect(rowOf(s, 'Riley Park').textContent).not.toContain("They aren't in the team any more.");
    expect(document.body.textContent).not.toMatch(/teammate|U02ALEX|spc_/i);
  });

  // Six "Test · Telegram" rows looked the same; their account ids tell them apart.
  it('tells apart people of one app with the same name and no username', async () => {
    const tm = (id: string, extra = {}) => ({ channel: 'telegram', external_id: id, name: 'Test', status: 'teammate', can_approve: false, left: false, last_seen_at: '', spent_today_micros: 0, ...extra });
    installApi({ override: (m, url) => (m === 'GET' && url === '/api/teammates' ? [tm('111'), tm('222'), tm('333', { handle: 'tester' }), tm('444', { channel: 'slack' })] : undefined) });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    settingsConfig.view = null;
    render(Settings, { params: { group: 'people', page: 'people' } });
    const s = await section();
    await waitFor(() => expect(s.getAllByRole('listitem')).toHaveLength(4));
    const metas = [...s.getAllByRole('listitem')].map((li) => li.querySelectorAll('p')[1]?.textContent ?? '');
    expect(metas[0]).toContain('Telegram · ID 111');
    expect(metas[1]).toContain('Telegram · ID 222');
    expect(metas[2]).not.toContain('ID 333');
    expect(metas[3]).not.toContain('ID 444');
  });

  it('keeps the rows where they are when a change here reloads the list', async () => {
    const calls = await open();
    const s = await section();
    await s.findByText('Sam', { selector: 'p' });
    const names = () => [...s.getAllByRole('listitem')].map((li) => li.querySelector('p')?.textContent);
    const before = names();
    await fireEvent.click(s.getByRole('switch', { name: 'Can approve: Morgan Reyes' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/identities/discord/1188221100445566')).toBe(true));
    // The server says inbox.changed after every change, and now lists Morgan among the approvers.
    const loads = calls.filter((c) => c.url === '/api/teammates').length;
    FakeEventSource.last.emit('inbox.changed', { seq: 2, type: 'inbox.changed', at: new Date().toISOString(), data: { item: 'sender:discord:1188221100445566' } });
    await waitFor(() => expect(calls.filter((c) => c.url === '/api/teammates')).toHaveLength(loads + 1));
    await new Promise((r) => setTimeout(r, 20));
    expect(names()).toEqual(before);
    expect(s.getByRole('switch', { name: 'Can approve: Morgan Reyes' }).getAttribute('aria-checked')).toBe('true');
  });

  it('moves Can approve into the menu on phones, and says in the row who can approve', async () => {
    window.matchMedia = ((q: string) => ({ matches: q === '(max-width: 639px)', media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
    try {
      const calls = await open();
      const s = await section();
      await s.findByText('Sam', { selector: 'p' });
      expect(s.queryByRole('switch')).toBeNull();
      expect(rowOf(s, 'Sam').textContent).toContain('@sam · Slack · Can approve');
      expect(rowOf(s, 'Alex Kim').textContent).not.toContain('Can approve');

      await fireEvent.click(s.getByRole('button', { name: 'Options for Alex Kim' }));
      let item = await screen.findByRole('menuitemcheckbox', { name: 'Can approve' });
      expect(item.getAttribute('aria-checked')).toBe('false');
      await fireEvent.click(item);
      await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/identities/slack/U02ALEX' && JSON.parse(c.body).can_approve === true)).toBe(true));
      await waitFor(() => expect(rowOf(s, 'Alex Kim').textContent).toContain('@alexk · Slack · Can approve'));
      await fireEvent.click(s.getByRole('button', { name: 'Options for Alex Kim' }));
      item = await screen.findByRole('menuitemcheckbox', { name: 'Can approve' });
      expect(item.getAttribute('aria-checked')).toBe('true');
      await fireEvent.keyDown(document.activeElement!, { key: 'Escape' });

      await fireEvent.click(s.getByRole('button', { name: 'Options for Riley Park' }));
      item = await screen.findByRole('menuitemcheckbox', { name: /^Can approve/ });
      expect(item.getAttribute('aria-disabled')).toBe('true');
      expect(item.textContent).toContain('No longer in Acme');
    } finally {
      delete (window as { matchMedia?: unknown }).matchMedia;
    }
  });

  it('lets someone approve with the switch, and says so', async () => {
    const calls = await open();
    const s = await section();
    const sw = await s.findByRole('switch', { name: 'Can approve: Alex Kim' });
    expect(sw.getAttribute('aria-checked')).toBe('false');
    await fireEvent.click(sw);
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/identities/slack/U02ALEX')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH' && c.url === '/api/identities/slack/U02ALEX')!.body)).toEqual({ can_approve: true });
    await waitFor(() => expect(sw.getAttribute('aria-checked')).toBe('true'));
  });

  it('puts the switch back and says so under the row when the change fails', async () => {
    installApi({ override: (m, url) => (m === 'PATCH' && url.startsWith('/api/identities/') ? new Response('{"error":{"code":"internal","message":"x"}}', { status: 500 }) : undefined) });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    settingsConfig.view = null;
    render(Settings, { params: { group: 'people', page: 'people' } });
    const s = await section();
    const sw = await s.findByRole('switch', { name: 'Can approve: Alex Kim' });
    await fireEvent.click(sw);
    expect(await within(rowOf(s, 'Alex Kim')).findByText("Couldn't save. Try again.")).toBeTruthy();
    await waitFor(() => expect(sw.getAttribute('aria-checked')).toBe('false'));
  });

  it('sets a daily limit for one person, keyed by their chat app', async () => {
    const calls = await open();
    const s = await section();
    await fireEvent.click(await s.findByRole('button', { name: 'Options for Alex Kim' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Daily limit…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Daily limit for Alex Kim' });
    const usual = within(dialog).getByRole('radio', { name: 'Use the default ($2.00 a day)' }) as HTMLInputElement;
    expect(usual.checked).toBe(true);
    // One native group, so the arrow keys move between the two and Tab stops once.
    expect(usual.name).toBeTruthy();
    expect((within(dialog).getByRole('radio', { name: 'Set a limit' }) as HTMLInputElement).name).toBe(usual.name);
    await fireEvent.click(within(dialog).getByRole('radio', { name: 'Set a limit' }));
    await fireEvent.input(within(dialog).getByLabelText('Daily limit'), { target: { value: '5' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body).set)).toContainEqual({ 'budgets.per_user_daily_usd.slack:U02ALEX': 5 }));
    await waitFor(() => expect(rowOf(s, 'Alex Kim').textContent).toMatch(/\$1\.15 of \$5\.00 today/));
  });

  it('saves the share of the daily limit chat apps may use', async () => {
    const calls = await open();
    const share = (await screen.findByLabelText('Share of the daily limit for chat apps')) as HTMLSelectElement;
    expect([...share.options].map((o) => o.text)).toEqual(['40%', '60% (recommended)', '80%', 'All of it']);
    expect(share.value).toBe('60');
    expect(screen.getByText("Leaves the rest of Bothy's $3.00 daily limit for the web.")).toBeTruthy();
    await fireEvent.change(share, { target: { value: '100' } });
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body).set)).toContainEqual({ 'budgets.chat_share_pct': 100 }));
  });

  it('blocks only after the confirmation, and unblocks', async () => {
    const calls = await open();
    const s = await section();
    await fireEvent.click(await s.findByRole('button', { name: 'Options for Morgan Reyes' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Block…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Block Morgan Reyes?' });
    expect(dialog.textContent).toContain("Bothy stops answering them in Discord and stops anything it's doing for them. Their chats are kept.");
    expect(calls.some((c) => c.method === 'PATCH' && c.url.startsWith('/api/identities/'))).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Block' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/identities/discord/1188221100445566' && JSON.parse(c.body).status === 'blocked')).toBe(true));
    await waitFor(() => expect(rowOf(s, 'Morgan Reyes').textContent).toContain('Discord · Blocked'));
    await fireEvent.click(s.getByRole('button', { name: 'Options for Morgan Reyes' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Unblock' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/identities/discord/1188221100445566' && JSON.parse(c.body).status === 'teammate')).toBe(true));
  });

  it("deletes a blocked person's data after a confirmation that says they stay blocked", async () => {
    const calls = await open();
    const s = await section();
    await fireEvent.click(await s.findByRole('button', { name: 'Options for Casey' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete their data…' }));
    const dialog = await screen.findByRole('dialog', { name: "Delete Casey's data?" });
    expect(dialog.textContent).toContain("This deletes their chats, files, notes and schedules in Bothy. It can't be undone. They stay blocked.");
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/teammates/telegram/8830')).toBe(true));
  });

  it('offers to delete the data of someone no longer in the team, but not of others', async () => {
    await open();
    const s = await section();
    await fireEvent.click(await s.findByRole('button', { name: 'Options for Riley Park' }));
    expect(await screen.findByRole('menuitem', { name: 'Delete their data…' })).toBeTruthy();
    await fireEvent.keyDown(document.activeElement!, { key: 'Escape' });
    await fireEvent.click(s.getByRole('button', { name: 'Options for Sam' }));
    await screen.findByRole('menuitem', { name: 'Block…' });
    expect(screen.queryByRole('menuitem', { name: 'Delete their data…' })).toBeNull();
  });

  it('says when nobody can approve in chat apps yet', async () => {
    installApi({ override: (m, url) => (m === 'GET' && url === '/api/teammates' ? [{ channel: 'telegram', external_id: '1', name: 'Jo', status: 'teammate', can_approve: false, left: false, last_seen_at: new Date().toISOString(), spent_today_micros: 0 }] : undefined) });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    settingsConfig.view = null;
    render(Settings, { params: { group: 'people', page: 'people' } });
    expect(await screen.findByText('Nobody can approve in chat apps yet, so requests from chat apps wait for an admin in Approvals.')).toBeTruthy();
  });

  it('finds someone by name once the list is long', async () => {
    const many = Array.from({ length: 22 }, (_, i) => ({ channel: 'slack', external_id: `U${i}`, name: `Person ${i}`, status: 'teammate', can_approve: i === 0, left: false, last_seen_at: new Date().toISOString(), spent_today_micros: 0 }));
    installApi({ override: (m, url) => (m === 'GET' && url === '/api/teammates' ? many : undefined) });
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    settingsConfig.view = null;
    render(Settings, { params: { group: 'people', page: 'people' } });
    const find = await screen.findByLabelText('Find someone');
    await fireEvent.input(find, { target: { value: 'person 21' } });
    const s = await section();
    expect(s.getAllByRole('listitem')).toHaveLength(1);
    await fireEvent.input(find, { target: { value: 'nobody' } });
    expect(await s.findByText('No one matches “nobody”.')).toBeTruthy();
  });

  it('says how to appear here when nobody has messaged Bothy in a chat app', async () => {
    await open('chat-people-none');
    const s = await section();
    expect(await s.findByText('Nobody uses Bothy in a chat app yet.')).toBeTruthy();
    // Telegram can't say who is in the team, so even an admin waits to be let in.
    expect(s.getByText('Message Bothy in Slack or Telegram to appear here. On Telegram, let yourself in from Approvals first.')).toBeTruthy();
    expect(s.getByRole('link', { name: 'Open Approvals' }).getAttribute('href')).toBe('/approvals');
  });

  it('sends Discord people to Approvals only while Bothy cannot check its members', async () => {
    await open('chat-people-none,discord-on');
    let s = await section();
    expect(await s.findByText('Message Bothy in Slack, Telegram or Discord to appear here. On Telegram, let yourself in from Approvals first.')).toBeTruthy();
    cleanup();
    await open('chat-people-none,discord-on:no-access');
    s = await section();
    expect(await s.findByText('Message Bothy in Slack, Telegram or Discord to appear here. On Telegram and Discord, let yourself in from Approvals first.')).toBeTruthy();
  });

  it('reminds admins after the upgrade that people in chat apps no longer link to accounts', async () => {
    await open('team-access');
    const s = await section();
    expect(await s.findByText('People in chat apps no longer link to Bothy accounts. Check who can use Bothy and who can approve below.')).toBeTruthy();
  });

  it('sends the admin to connect a chat app when none is', async () => {
    await open('chat-people-none,chat-apps-off');
    const s = await section();
    expect(await s.findByText('No chat app is connected yet.')).toBeTruthy();
    expect(s.getByRole('link', { name: 'Connect a chat app' }).getAttribute('href')).toBe('/settings/connections');
    expect(s.queryByText(/^Nobody has messaged Bothy/)).toBeNull();
    // No intro about approving and no share of a limit for apps that aren't there.
    expect(s.queryByText(/^People who can approve/)).toBeNull();
    expect(s.queryByLabelText('Share of the daily limit for chat apps')).toBeNull();
  });
});
