import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { toaster } from '../../../components/ui';
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

async function enterToken(value = TOKEN) {
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
    await fireEvent.click(await screen.findByRole('radio', { name: 'GitHub App' }));
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
