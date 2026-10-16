import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { resetMock } from '../../../mock/server';
import { setCsrf } from '../../lib/api/client';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi } from '../../test/api';
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

  it('sets and clears a personal spending limit by username', async () => {
    const calls = await open();
    let m = await menu('Alex');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Spending limit…' }));
    let dialog = await screen.findByRole('dialog', { name: 'Spending limit' });
    expect(within(dialog).getByText('Members without their own limit can spend up to $2.00 a day.')).toBeTruthy();
    await fireEvent.click(within(dialog).getByLabelText('No personal limit'));
    await fireEvent.input(within(dialog).getByLabelText('Daily limit for Alex'), { target: { value: '3' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body).set)).toContainEqual({ 'budgets.per_user_daily_usd.alex': 3 }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Spending limit' })).toBeNull());

    m = await menu('Alex');
    await fireEvent.click(within(m).getByRole('menuitem', { name: 'Spending limit…' }));
    dialog = await screen.findByRole('dialog', { name: 'Spending limit' });
    expect((within(dialog).getByLabelText('Daily limit for Alex') as HTMLInputElement).value).toBe('3.00');
    await fireEvent.click(within(dialog).getByLabelText('No personal limit'));
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

  it("opens Add person when setup's \"Add people\" sends someone here", async () => {
    router.navigate('/settings/people?add=1');
    await open();
    expect(await screen.findByRole('dialog', { name: 'Add person' })).toBeTruthy();
    expect(router.search).toBe('');
  });

  it('invites the owner to add people when there is nobody else', async () => {
    await open('people-solo');
    expect(await screen.findByText("It's just you so far. Add people so they get their own sign-in.")).toBeTruthy();
    expect(screen.getAllByRole('button', { name: 'Add person' })).toHaveLength(1);
    // People in chat apps show whatever the accounts are.
    expect(await screen.findByRole('heading', { name: 'In chat apps' })).toBeTruthy();
  });
});

describe('Settings → People → In chat apps', () => {
  const section = async () => within((await screen.findByRole('heading', { name: 'In chat apps' })).closest('section')!);
  const rowOf = (s: Awaited<ReturnType<typeof section>>, name: string) => s.getByText(name, { selector: 'p' }).closest('li')!;

  it('lists approvers first, then by when they were last active, then blocked people, with spending today', async () => {
    await open();
    const s = await section();
    await s.findByText('Sam', { selector: 'p' });
    expect(s.getByText(/^People who can approve get requests from everyone in chat apps/)).toBeTruthy();
    const names = [...s.getAllByRole('listitem')].map((li) => li.querySelector('p')?.textContent);
    expect(names).toEqual(['Sam', 'Alex Kim', 'Morgan Reyes', 'Riley Park', 'Casey']);
    expect(rowOf(s, 'Alex Kim').textContent).toContain('@alexk · Slack');
    expect(rowOf(s, 'Alex Kim').textContent).toMatch(/Active .+ · \$1\.15 of \$2\.00 today/);
    expect(rowOf(s, 'Riley Park').textContent).toContain('@riley · Slack · No longer in Acme');
    expect(rowOf(s, 'Casey').textContent).toContain('Telegram · Blocked');
    expect(within(rowOf(s, 'Casey')).queryByRole('switch')).toBeNull();
    expect(within(rowOf(s, 'Riley Park')).getByRole('switch', { name: 'Can approve: Riley Park' }).getAttribute('aria-disabled')).toBe('true');
    expect(document.body.textContent).not.toMatch(/teammate|U02ALEX|spc_/i);
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
    expect((within(dialog).getByRole('radio', { name: 'Use the default ($2.00)' }) as HTMLInputElement).checked).toBe(true);
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
    expect(await screen.findByText('Nobody has messaged Bothy in a chat app yet. Message Bothy in a chat app to appear here.')).toBeTruthy();
  });
});
