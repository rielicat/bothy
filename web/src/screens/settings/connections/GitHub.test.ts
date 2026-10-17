import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../../mock/server';
import { toaster } from '../../../components/ui';
import { away } from '../../../lib/away';
import { router } from '../../../lib/router.svelte';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';
import EnvironmentDialog from './EnvironmentDialog.svelte';
import GitHub from './GitHub.svelte';

const TOKEN = 'github_pat_11ABCDEFG0123456789abcdef';

async function setup(override?: (m: string, url: string) => unknown): Promise<Call[]> {
  const calls = installApi({ override });
  await session.init();
  render(GitHub);
  return calls;
}

const none = (more?: (m: string, url: string) => unknown) => (m: string, url: string) => more?.(m, url) ?? (m === 'GET' && url === '/api/github/connections' ? [] : undefined);

// The token and existing-app paths live under "Other ways to connect".
async function openOther() {
  await fireEvent.click(await screen.findByRole('button', { name: 'Other ways to connect' }));
}

async function enterToken(value = TOKEN) {
  if (!screen.queryByLabelText('Token')) await openOther();
  const field = await screen.findByLabelText('Token');
  await fireEvent.input(field, { target: { value } });
  await fireEvent.keyDown(field, { key: 'Enter' });
}

describe('Connect GitHub (§12.2)', () => {
  it('checks a token, lists what it can see and saves the chosen repositories', async () => {
    const calls = await setup(none());
    expect(await screen.findByText('Bothy uses this to read your code and open pull requests. It only ever works on its own branches.')).toBeTruthy();
    await enterToken();
    expect(await screen.findByText('Connected as maya-acme. Bothy can see 6 repositories.')).toBeTruthy();
    expect(JSON.parse(calls.find((c) => c.url === '/api/github/connections/test')!.body)).toEqual({ auth: 'pat', token: TOKEN });

    // Nothing is chosen for Bothy until someone ticks it; Save says why it waits.
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save.getAttribute('aria-disabled')).toBe('true');
    expect(screen.getByText('Choose at least one repository.')).toBeTruthy();
    expect(screen.queryByLabelText('acme/legacy-billing')).toBeNull();

    await fireEvent.click(screen.getByLabelText('acme/web'));
    await fireEvent.click(save);
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/github/connections')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'POST' && c.url === '/api/github/connections')!.body)).toEqual({ auth: 'pat', token: TOKEN, repos: ['acme/web'] });
  });

  it('filters the repository list and selects what is shown', async () => {
    await setup(none());
    await enterToken();
    await screen.findByText(/Bothy can see/);
    await fireEvent.input(screen.getByRole('searchbox', { name: 'Filter repositories' }), { target: { value: 'terraform' } });
    expect(screen.getAllByRole('checkbox').map((c) => c.closest('label')?.textContent?.trim().split(/\s+/)[0])).toEqual(['acme/infra']);
    await fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
    expect((screen.getByLabelText('acme/infra') as HTMLInputElement).checked).toBe(true);
  });

  it('says plainly when GitHub refuses the token', async () => {
    await setup(none());
    await enterToken('github_pat_bad0123456789abcdefghij');
    expect((await screen.findByRole('alert')).textContent).toContain("GitHub didn't accept that token. It may have expired or been revoked.");
    expect(screen.queryByRole('heading', { name: 'Which repositories can Bothy use?' })).toBeNull();
  });

  it('says when the token sees no repositories', async () => {
    await setup(none());
    await enterToken('github_pat_empty0123456789abcdefgh');
    expect(await screen.findByText("The token works but can't see any repositories. Give it access to at least one repository on GitHub.")).toBeTruthy();
  });

  it('checks a GitHub App from its ID and private key', async () => {
    const calls = await setup(none());
    await openOther();
    await fireEvent.click(await screen.findByRole('radio', { name: 'A GitHub App you already have' }));
    await fireEvent.input(screen.getByLabelText('App ID'), { target: { value: '123456' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Paste instead' }));
    await fireEvent.input(screen.getByLabelText('Private key'), { target: { value: '-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----' } });
    expect(await screen.findByText('Installed on acme · 6 repositories', {}, { timeout: 2000 })).toBeTruthy();
    expect(JSON.parse(calls.find((c) => c.url === '/api/github/connections/test')!.body)).toMatchObject({ auth: 'app', app_id: '123456' });
  });
});

describe('GitHub manage view', () => {
  it('shows the account, the repositories and the deploy environments', async () => {
    await setup();
    expect(await screen.findByText('Connected as acme-bot (personal access token)')).toBeTruthy();
    expect(await screen.findByText('acme/web')).toBeTruthy();
    expect(screen.getByText('Not in use')).toBeTruthy();
    expect(await screen.findByText('acme/web · Deploy workflow · 2 approvals from Alex, any admin')).toBeTruthy();
    expect(screen.getByText('acme/web · Deploy workflow · 1 approval from any admin')).toBeTruthy();
  });

  // When GitHub can't list the workflows, the row names the workflow file.
  it('names the workflow file when GitHub workflows are not listed', async () => {
    await setup((m, url) => (url.endsWith('/workflows') ? new Response('{"error":{"code":"not_found","message":"no route"}}', { status: 404 }) : undefined));
    expect(await screen.findByText('acme/web · deploy.yml · 1 approval from any admin')).toBeTruthy();
  });

  it('changes which repositories Bothy may use with one switch each', async () => {
    const calls = await setup();
    await fireEvent.click(await screen.findByRole('switch', { name: 'Bothy can use acme/infra' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PUT')!.body).repos.sort()).toEqual(['acme/api', 'acme/infra', 'acme/web']);
  });

  it('leaves repositories already listed out of Add repositories, even when they are off', async () => {
    await setup();
    expect(await screen.findByRole('switch', { name: 'Bothy can use acme/infra' })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Add repositories' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add repositories' });
    expect(await within(dialog).findByLabelText('acme/mobile')).toBeTruthy();
    for (const listed of ['acme/web', 'acme/api', 'acme/infra']) expect(within(dialog).queryByLabelText(listed)).toBeNull();
  });

  // A repository belongs to one connection, so replacing the token removes
  // the old connection before the new one claims the repositories.
  it('replaces the token by disconnecting first, then connecting again', async () => {
    const calls = await setup();
    await fireEvent.click(await screen.findByRole('button', { name: 'Replace token' }));
    await enterToken();
    await screen.findByText(/Bothy can see/);
    await fireEvent.click(screen.getByLabelText('acme/web'));
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/github/connections')).toBe(true));
    const writes = calls.filter((c) => c.method === 'DELETE' || (c.method === 'POST' && c.url === '/api/github/connections')).map((c) => c.method);
    expect(writes).toEqual(['DELETE', 'POST']);
  });

  it('starts reading a repository again from its menu', async () => {
    const calls = await setup();
    await fireEvent.click(await screen.findByRole('button', { name: 'Options for acme/web' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Read again' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/reindex'))).toBe(true));
    await waitFor(() => expect(toaster.current?.text).toBe('Started reading again.'));
  });

  it('says deleting an environment is permanent before it does', async () => {
    const calls = await setup();
    await fireEvent.click(await screen.findByRole('button', { name: 'Options for staging' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete environment…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete staging?' });
    expect(within(dialog).getByText("Bothy can no longer deploy to staging. Past deploys stay in Activity. This can't be undone.")).toBeTruthy();
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('disconnects only after the confirmation', async () => {
    const calls = await setup();
    await fireEvent.click(await screen.findByRole('button', { name: 'Disconnect GitHub…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Disconnect GitHub?' });
    expect(within(dialog).getByText('Bothy will stop reading these repositories and can\'t open pull requests or deploy. Project notes are kept.')).toBeTruthy();
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Disconnect GitHub' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'Connect GitHub' })).toBeTruthy();
  });
});

describe("Bothy's own GitHub App (§12.2)", () => {
  const real = away.to;
  let left: string[] = [];
  beforeEach(() => {
    left = [];
    away.to = (url: string) => void left.push(url);
  });
  afterEach(() => {
    away.to = real;
    router.navigate('/');
  });

  async function app(mock: string, path = '/settings/connections/github', override?: (m: string, url: string) => unknown): Promise<Call[]> {
    const calls = installApi({ override });
    resetMock({ mock });
    await session.init();
    router.navigate(path);
    render(GitHub);
    return calls;
  }

  const posts = (calls: Call[], url: string) => calls.filter((c) => c.method === 'POST' && c.url === url);

  it('offers creating an app first, for an organization, and waits for its name', async () => {
    await app('github-none');
    expect(await screen.findByRole('heading', { name: 'Create a GitHub App for Bothy' })).toBeTruthy();
    expect((screen.getByRole('radio', { name: 'An organization' }) as HTMLInputElement).checked).toBe(true);
    const go = screen.getByRole('button', { name: 'Continue on GitHub' });
    expect(go.getAttribute('aria-disabled')).toBe('true');
    expect(screen.getByText("Enter the organization's name.")).toBeTruthy();
    await fireEvent.input(screen.getByLabelText('Organization'), { target: { value: 'acme' } });
    await fireEvent.click(screen.getByRole('button', { name: "Change the app's name" }));
    expect((screen.getByLabelText('App name') as HTMLInputElement).placeholder).toBe('Bothy (acme)');
    expect(screen.queryByLabelText('Token')).toBeNull();
  });

  it('proposes a name no app Bothy recorded has', async () => {
    await app('github-app-retired');
    await fireEvent.input(await screen.findByLabelText('Organization'), { target: { value: 'acme' } });
    await fireEvent.click(screen.getByRole('button', { name: "Change the app's name" }));
    expect((screen.getByLabelText('App name') as HTMLInputElement).placeholder).toBe('Bothy (acme) 2');
  });

  it('starts on GitHub for an organization', async () => {
    const calls = await app('github-none');
    await fireEvent.input(await screen.findByLabelText('Organization'), { target: { value: 'acme' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));
    await waitFor(() => expect(left).toEqual(['/api/github/apps/handoff']));
    expect(JSON.parse(posts(calls, '/api/github/apps')[0].body)).toEqual({ account: 'organization', org: 'acme' });
    // The button keeps saying so while the browser leaves for GitHub.
    expect(screen.getByRole('button', { name: 'Opening GitHub…' })).toBeTruthy();
  });

  it('starts on GitHub for a personal account', async () => {
    const calls = await app('github-none');
    await fireEvent.click(await screen.findByRole('radio', { name: 'Your personal account' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));
    await waitFor(() => expect(left).toEqual(['/api/github/apps/handoff']));
    expect(JSON.parse(posts(calls, '/api/github/apps')[0].body)).toEqual({ account: 'personal' });
  });

  it("refuses a name that can't be an organization's", async () => {
    const calls = await app('github-none');
    await fireEvent.input(await screen.findByLabelText('Organization'), { target: { value: 'acme inc' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));
    expect(await screen.findByText("That isn't an organization name on GitHub. Use the name from its address, like acme in github.com/acme.")).toBeTruthy();
    expect(posts(calls, '/api/github/apps')).toHaveLength(0);
  });

  it.each([
    ['wrong_address', 'Open Bothy at https://bothy.acme.dev to create the app. GitHub sends you back to that address.'],
    ['sealed', "Bothy's saved keys are locked. Unlock them, then try again."],
  ])('says why it could not start (%s)', async (code, text) => {
    await app(`github-none,github-app-start-fail:${code}`);
    await fireEvent.input(await screen.findByLabelText('Organization'), { target: { value: 'acme' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));
    expect((await screen.findByRole('alert')).textContent).toContain(text);
    expect(left).toEqual([]);
  });

  it('shows the app waiting to be installed when another start already made one', async () => {
    await app('github-none,github-app-start-fail:conflict');
    await fireEvent.input(await screen.findByLabelText('Organization'), { target: { value: 'acme' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));
    expect(await screen.findByRole('heading', { name: 'Finish installing Bothy (acme) on GitHub' })).toBeTruthy();
  });

  it('asks to finish installing a pending app, checks again and starts over', async () => {
    const calls = await app('github-app-pending');
    expect(await screen.findByRole('heading', { name: 'Finish installing Bothy (acme) on GitHub' })).toBeTruthy();
    expect(screen.getByText('GitHub created the app. Install it on acme and choose the repositories Bothy can use.')).toBeTruthy();
    expect(screen.getByRole('link', { name: /Install on GitHub/ }).getAttribute('href')).toBe('https://github.com/apps/bothy-acme/installations/new');
    expect(screen.queryByRole('button', { name: 'Continue on GitHub' })).toBeNull();
    // One quiet check on load, then Check again says what it found.
    await waitFor(() => expect(posts(calls, '/api/github/apps/gha_01jacme00000000000000000000/check')).toHaveLength(1));
    await fireEvent.click(screen.getByRole('button', { name: 'Check again' }));
    expect(await screen.findByText("Bothy (acme) isn't installed yet.")).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Start over…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Start over?' });
    expect(within(dialog).getByText('Bothy stops using Bothy (acme) and deletes its keys. The app stays on GitHub until you delete it there.')).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Start over' }));
    expect(await screen.findByText('Bothy no longer uses Bothy (acme). Delete it on GitHub too.')).toBeTruthy();
    expect(screen.getByRole('link', { name: /Open app settings/ }).getAttribute('href')).toBe('https://github.com/organizations/acme/settings/apps/bothy-acme');
    expect(screen.getByRole('button', { name: 'Continue on GitHub' })).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/github/apps/gha_01jacme00000000000000000000')).toBe(true));
    await waitFor(() => expect(screen.queryByText('Bothy no longer uses Bothy (acme). Delete it on GitHub too.')).toBeNull());
  });

  it('connects once the pending app is installed and asks which repositories to use', async () => {
    await app('github-app-installs');
    expect(await screen.findByRole('dialog', { name: 'Which repositories can Bothy use?' })).toBeTruthy();
    expect(screen.getByText('Connected to acme as Bothy (acme)')).toBeTruthy();
  });

  it('opens the repository picker when GitHub sends the browser back installed', async () => {
    const calls = await app('github-app-made', '/settings/connections/github?github_app=installed');
    const dialog = await screen.findByRole('dialog', { name: 'Add repositories' });
    expect(router.query.has('github_app')).toBe(false);
    await fireEvent.click(await within(dialog).findByLabelText('acme/docs'));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add repositories' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    expect(await screen.findByText('Bothy is reading 1 repository. You can keep working; this takes a few minutes.')).toBeTruthy();
  });

  it("says so instead of opening the picker when there's nothing new to add", async () => {
    await app('github-app-made', '/settings/connections/github?github_app=installed', (m, url) =>
      m === 'POST' && /^\/api\/github\/connections\/[^/]+\/test$/.test(url) ? { ok: true, auth: 'app', account: 'acme', repos: [] } : undefined,
    );
    await waitFor(() => expect(toaster.current?.text).toMatch(/^Connected\. Bothy keeps using \d+ repositor(y|ies)\.$/));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it.each([
    ['not_finished', 'acme', "Bothy couldn't finish setting up the app: too much time passed, you signed out, or you started in another browser. Delete the new app on GitHub, then start again.", 'https://github.com/organizations/acme/settings/apps'],
    ['exchange_failed', 'me', "GitHub created the app, but Bothy couldn't get its keys from GitHub. Delete the new app on GitHub, then start again.", 'https://github.com/settings/apps'],
    ['keep_failed', 'acme', "Bothy couldn't keep the new app's keys, so it can't use it. Delete the new app on GitHub, then start again.", 'https://github.com/organizations/acme/settings/apps'],
    ['sealed', 'acme', "Bothy's saved keys are locked, so it couldn't keep the new app's keys. Unlock them and delete the new app on GitHub, then start again.", 'https://github.com/organizations/acme/settings/apps'],
    ['expired', '', 'That link to GitHub expired. Start again.', ''],
    ['unavailable', '', "Bothy can't connect GitHub right now. Try again later.", ''],
  ])('says what happened when the round trip ends as %s', async (code, apps, text, link) => {
    await app('github-none', `/settings/connections/github?github_app=${code}${apps ? `&apps=${apps}` : ''}`);
    expect((await screen.findByRole('alert')).textContent).toContain(text);
    const a = screen.queryByRole('link', { name: /GitHub Apps/ });
    expect(a?.getAttribute('href') ?? '').toBe(link);
    expect(router.query.has('github_app') || router.query.has('apps')).toBe(false);
  });

  it('drops a failed app round trip once a token connects instead', async () => {
    await app('github-none', '/settings/connections/github?github_app=keep_failed&apps=acme');
    const failed = "Bothy couldn't keep the new app's keys, so it can't use it. Delete the new app on GitHub, then start again.";
    expect((await screen.findByRole('alert')).textContent).toContain(failed);
    await enterToken();
    await screen.findByText(/Bothy can see/);
    await fireEvent.click(screen.getByLabelText('acme/web'));
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByText(failed)).toBeNull());
  });

  it("says when GitHub's installation couldn't be confirmed", async () => {
    await app('github-app-pending', '/settings/connections/github?github_app=install_failed');
    expect((await screen.findByRole('alert')).textContent).toContain("Bothy couldn't confirm the app's installation with GitHub. Choose Check again, or install it again.");
  });

  it('says an owner has to approve a requested installation', async () => {
    await app('github-app-pending', '/settings/connections/github?github_app=requested');
    expect(await screen.findByText('An owner of acme needs to approve Bothy (acme) on GitHub. Once they have, choose Check again.')).toBeTruthy();
  });

  it('names the app Bothy made and where its installation is managed', async () => {
    const calls = await app('github-app-made');
    expect(await screen.findByText('Connected to acme as Bothy (acme)')).toBeTruthy();
    expect(screen.getByText('Pull requests and comments show as bothy-acme[bot].')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Add repositories' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add repositories' });
    expect((await within(dialog).findByRole('link', { name: /Give the app access on GitHub/ })).getAttribute('href')).toBe(
      'https://github.com/organizations/acme/settings/installations/5151',
    );
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await fireEvent.click(screen.getByRole('button', { name: 'Disconnect GitHub…' }));
    const leave = await screen.findByRole('dialog', { name: 'Disconnect GitHub?' });
    expect(leave.textContent).toContain('Bothy (acme) stays on GitHub until you delete it there.');
    await fireEvent.click(within(leave).getByRole('button', { name: 'Disconnect GitHub' }));
    expect(await screen.findByText('Bothy no longer uses Bothy (acme). Delete it on GitHub too.')).toBeTruthy();
    expect(calls.some((c) => c.method === 'DELETE' && c.url.startsWith('/api/github/connections/'))).toBe(true);
  });

  it('says when switching to a replacement app failed and tries again', async () => {
    const id = 'gha_01jops00000000000000000000';
    let switched = false;
    const stuck = { id, name: 'Bothy (ops)', state: 'connected', connection_id: 'ghc_01jops00000000000000000000', replaces: 'ghc_01jacme00000000000000000000' };
    const calls = await app('', '/settings/connections/github?github_app=installed', (m, url) => {
      if (m === 'GET' && url === '/api/github/apps') return switched ? [] : [stuck];
      if (m === 'POST' && url === `/api/github/apps/${id}/check`) return (switched = true), { installed: true };
      return undefined;
    });
    expect(await screen.findByText(/Bothy couldn't switch to Bothy \(ops\) yet, so it still uses this connection\./)).toBeTruthy();
    // The landing doesn't open the picker on the connection being replaced.
    await expect(screen.findByRole('dialog', {}, { timeout: 500 })).rejects.toThrow();
    await fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await waitFor(() => expect(posts(calls, `/api/github/apps/${id}/check`)).toHaveLength(1));
    await waitFor(() => expect(screen.queryByText(/Bothy couldn't switch/)).toBeNull());
  });

  it('says when Try again finds the replacement app no longer installed on its owner', async () => {
    const id = 'gha_01jops00000000000000000000';
    const stuck = {
      id,
      name: 'Bothy (ops)',
      owner: 'acme',
      owner_type: 'Organization',
      state: 'connected',
      connection_id: 'ghc_01jops00000000000000000000',
      replaces: 'ghc_01jacme00000000000000000000',
      install_url: 'https://github.com/apps/bothy-ops/installations/new',
    };
    const calls = await app('', '/settings/connections/github', (m, url) => {
      if (m === 'GET' && url === '/api/github/apps') return [stuck];
      if (m === 'POST' && url === `/api/github/apps/${id}/check`) return { installed: false };
      return undefined;
    });
    await fireEvent.click(await screen.findByRole('button', { name: 'Try again' }));
    expect(await screen.findByText(/Bothy \(ops\) isn't installed on acme any more\./)).toBeTruthy();
    expect(screen.getByRole('link', { name: /Install on GitHub/ }).getAttribute('href')).toBe(stuck.install_url);
    expect((await screen.findByRole('alert')).textContent).toContain("Bothy couldn't switch to Bothy (ops) yet, so it still uses this connection.");
    expect(posts(calls, `/api/github/apps/${id}/check`)).toHaveLength(1);
  });

  const replacement = {
    id: 'gha_01jops00000000000000000000',
    name: 'Bothy (ops)',
    owner: 'acme',
    owner_type: 'Organization',
    state: 'pending',
    replaces: 'ghc_01jacme00000000000000000000',
    install_url: 'https://github.com/apps/bothy-ops/installations/new',
  };
  const withReplacement = (m: string, url: string) => (m === 'GET' && url === '/api/github/apps' ? [replacement] : undefined);

  it('says in the manage view that an owner has to approve the replacement app', async () => {
    await app('', '/settings/connections/github?github_app=requested', withReplacement);
    expect(await screen.findByText('An owner of acme needs to approve Bothy (ops) on GitHub. Once they have, choose Check again.')).toBeTruthy();
    expect(screen.getByText('Connected as acme-bot (personal access token)')).toBeTruthy();
  });

  it("says in the manage view when the replacement app's installation couldn't be confirmed", async () => {
    await app('', '/settings/connections/github?github_app=install_failed', withReplacement);
    expect((await screen.findByRole('alert')).textContent).toContain("Bothy couldn't confirm the app's installation with GitHub. Choose Check again, or install it again.");
    expect(screen.getByText('Finish installing Bothy (ops) on GitHub to switch Bothy to it.')).toBeTruthy();
  });

  it('says the app Bothy made still is not installed when checking a broken connection', async () => {
    const calls = await app('github-app-broken');
    expect(await screen.findByText('GitHub stopped accepting Bothy (acme). If it was uninstalled, install it again.')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Check again' }));
    expect(await screen.findByText("Bothy (acme) isn't installed yet.")).toBeTruthy();
    expect(posts(calls, '/api/github/apps/gha_01jacme00000000000000000000/check')).toHaveLength(1);
    expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/test'))).toBe(false);
  });

  it('creates a replacement app for an existing connection', async () => {
    const calls = await app('');
    await fireEvent.click(await screen.findByRole('button', { name: 'Replace token' }));
    await fireEvent.input(await screen.findByLabelText('Organization'), { target: { value: 'acme' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));
    await waitFor(() => expect(posts(calls, '/api/github/apps')).toHaveLength(1));
    expect(JSON.parse(posts(calls, '/api/github/apps')[0].body)).toEqual({ account: 'organization', org: 'acme', replaces: 'ghc_01jacme00000000000000000000' });
  });
});

describe('Environment dialog', () => {
  const users = [
    { id: 'usr_01jsam0000000000000000000', username: 'sam', display_name: 'Sam', role: 'owner', timezone: '', disabled: false, created_at: '', updated_at: '', last_login_at: '' },
    { id: 'usr_01jalex000000000000000000', username: 'alex', display_name: 'Alex', role: 'member', timezone: '', disabled: false, created_at: '', updated_at: '', last_login_at: '' },
  ];

  async function open(props: Record<string, unknown> = {}, override?: (m: string, url: string) => unknown) {
    const calls = installApi({ override });
    await session.init();
    let saved = 0;
    render(EnvironmentDialog, { open: true, repos: ['acme/web', 'acme/api'], users, taken: ['staging'], onsaved: () => saved++, ondelete: () => {}, ...props });
    return { calls, saved: () => saved };
  }

  it('starts with one approval from any admin and the deploy workflow', async () => {
    await open();
    const dialog = await screen.findByRole('dialog', { name: 'Add environment' });
    expect(((await within(dialog).findByRole('combobox', { name: 'Deploy workflow' })) as HTMLSelectElement).value).toBe('deploy.yml');
    expect(within(dialog).getByText('Any admin')).toBeTruthy();
    expect(within(dialog).getByRole('radio', { name: '1' }).getAttribute('aria-checked')).toBe('true');
    expect((within(dialog).getByLabelText('Branches') as HTMLInputElement).value).toBe('main');
  });

  it('explains two approvals and wants two people for them', async () => {
    const { calls } = await open();
    const dialog = await screen.findByRole('dialog');
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'production' } });
    await fireEvent.click(within(dialog).getByRole('radio', { name: '2' }));
    expect(within(dialog).getByText('Two different people must approve, and neither can be the person who asked.')).toBeTruthy();

    // Swap "Any admin" for one person: two approvals can't be met.
    const who = within(dialog).getByRole('combobox', { name: 'Who can approve' });
    await fireEvent.keyDown(who, { key: 'Backspace' });
    await fireEvent.input(who, { target: { value: 'ale' } });
    await fireEvent.keyDown(who, { key: 'Enter' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save environment' }));
    expect(await within(dialog).findByText('Choose at least two people, or Any admin.')).toBeTruthy();
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('moves focus to the first field that needs fixing', async () => {
    await open();
    const dialog = await screen.findByRole('dialog', { name: 'Add environment' });
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'qa' } });
    await fireEvent.input(within(dialog).getByLabelText('Branches'), { target: { value: ' ' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save environment' }));
    await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByLabelText('Branches')));
  });

  it('saves through config and renames by removing the old name', async () => {
    const env = { name: 'qa', repo: 'acme/api', workflow: 'deploy.yml', ref_pattern: 'main', inputs: { region: 'eu' }, required: 1, approvers: [{ kind: 'role', id: 'admin', name: 'admin' }] };
    const { calls, saved } = await open({ env, taken: [] });
    const dialog = await screen.findByRole('dialog', { name: 'Edit qa' });
    const name = within(dialog).getByLabelText('Name');
    await fireEvent.input(name, { target: { value: 'qa-eu' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save environment' }));
    await waitFor(() => expect(saved()).toBe(1));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set).toEqual({
      'releases.environments.qa-eu': { name: 'qa-eu', repo: 'acme/api', workflow: 'deploy.yml', ref_pattern: 'main', approvers: ['admin'], required: 1, inputs: { region: 'eu' } },
      'releases.environments.qa': null,
    });
  });

  it('says what to do when the server turns the environment down, with its reason under Details', async () => {
    const refused = { fields: [{ path: 'releases.environments.qa.workflow', message: 'workflow not found in acme/web' }] };
    await open({ taken: [] }, (m, url) => (m === 'PATCH' && url === '/api/config' ? new Response(JSON.stringify(refused), { status: 400 }) : undefined));
    const dialog = await screen.findByRole('dialog', { name: 'Add environment' });
    await waitFor(() => expect((within(dialog).getByRole('combobox', { name: 'Deploy workflow' }) as HTMLSelectElement).value).toBe('deploy.yml'));
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'qa' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save environment' }));
    expect(await within(dialog).findByText("Bothy couldn't save this environment. Check the details and try again.")).toBeTruthy();
    expect(within(dialog).queryByText('workflow not found in acme/web')).toBeNull();
  });
});
