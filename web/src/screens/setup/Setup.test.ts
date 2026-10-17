import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { setCsrf } from '../../lib/api/client';
import { session } from '../../lib/stores/session.svelte';
import { resetMock } from '../../../mock/server';
import { installApi, type Call } from '../../test/api';
import Setup from './Setup.svelte';

// installApi resets the fixture gateway; scenarios go on afterwards.
function withMock(mock: string): Call[] {
  const calls = installApi({ setup: true });
  resetMock({ setup: true, mock });
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET' && /\/api\/(config|vault|providers\/connect)/.test(c.url));

async function fillAccount(name = 'Maya Chen', username = 'maya', password = 'Granite-River-42-Lantern') {
  await fireEvent.input(await screen.findByLabelText('Your name'), { target: { value: name } });
  await fireEvent.input(screen.getByLabelText('Username'), { target: { value: username } });
  await fireEvent.input(screen.getByLabelText('Password'), { target: { value: password } });
}

const submitAccount = () => fireEvent.click(screen.getByRole('button', { name: 'Create account' }));

describe('setup code', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi({ setup: true });
    session.me = null;
    session.finishing = false;
    await session.init();
  });

  it('claims the code from the URL fragment and strips it at once', async () => {
    history.replaceState(null, '', '/setup#t=demo-setup-token');
    render(Setup);
    expect(location.hash).toBe('');
    expect(await screen.findByText('Create your account')).toBeTruthy();
    expect(screen.getByText('Step 1 of 3')).toBeTruthy();
    const claim = calls.find((c) => c.url === '/api/setup/claim');
    expect(JSON.parse(claim!.body)).toEqual({ token: 'demo-setup-token' });
    expect(calls.every((c) => !c.url.includes('demo-setup-token'))).toBe(true);
  });

  it('shows the command for this install and the other two behind a disclosure', async () => {
    history.replaceState(null, '', '/setup');
    render(Setup);
    expect(screen.getByText('Set up Bothy')).toBeTruthy();
    expect(screen.queryByText(/Step \d of 3/)).toBeNull();
    expect(screen.getByText('docker compose exec bothy /bothy setup-token')).toBeTruthy();
    expect(screen.queryByText('docker exec bothy /bothy setup-token')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Not using Docker Compose?' }));
    expect(screen.getByText('docker exec bothy /bothy setup-token')).toBeTruthy();
    expect(screen.getByText('bothy setup-token')).toBeTruthy();
    expect(screen.getByText('Installed directly')).toBeTruthy();
  });

  it('asks for the code, then says a wrong one did not work', async () => {
    history.replaceState(null, '', '/setup');
    render(Setup);
    const field = screen.getByLabelText('Setup code');
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    expect(screen.getByText('Paste the setup code first.')).toBeTruthy();
    expect(document.activeElement).toBe(field);
    await fireEvent.input(field, { target: { value: 'nope' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    expect(await screen.findByText("That code didn't work. It may have expired, so run the command below to get a new one.")).toBeTruthy();
    expect(field.getAttribute('aria-invalid')).toBe('true');
  });

  it('strips a mangled fragment and shows the code screen with the error', async () => {
    history.replaceState(null, '', '/setup#t=%E0%A4%A');
    render(Setup);
    expect(location.hash).toBe('');
    expect(await screen.findByText(/That code didn't work/)).toBeTruthy();
  });

  it('resumes at the account step after a reload, with the grant CSRF token', async () => {
    history.replaceState(null, '', '/setup#t=demo-setup-token');
    const first = render(Setup);
    await screen.findByText('Create your account');
    first.unmount();

    setCsrf('');
    await session.init();
    expect(session.setup?.claimed).toBe(true);
    render(Setup);
    await fillAccount();
    await submitAccount();
    await waitFor(() => expect(calls.some((c) => c.url === '/api/setup/owner')).toBe(true));
    expect(calls.find((c) => c.url === '/api/setup/owner')!.headers['x-csrf-token']).toBe('mock-setup-csrf');
    expect(await screen.findByText('Connect an AI model')).toBeTruthy();
    // Team mode and the time zone are written with the owner's own session token.
    const patch = calls.find((c) => c.method === 'PATCH' && c.url === '/api/config')!;
    expect(patch.headers['x-csrf-token']).toBe('mock-csrf');
  });
});

describe('step 1: create your account', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi({ setup: true });
    session.me = null;
    session.finishing = false;
    await session.init();
    history.replaceState(null, '', '/setup#t=demo-setup-token');
  });

  it('validates on submit, then live, and focuses the first field with an error', async () => {
    render(Setup);
    await screen.findByText('Create your account');
    await submitAccount();
    expect(screen.getByText('Enter your name.')).toBeTruthy();
    expect(screen.getByText(/Use up to 32 letters/)).toBeTruthy();
    expect(screen.getByText('Use at least 12 characters.')).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText('Your name'));
    await fireEvent.input(screen.getByLabelText('Your name'), { target: { value: 'Maya' } });
    expect(screen.queryByText('Enter your name.')).toBeNull();
    await fireEvent.input(screen.getByLabelText('Username'), { target: { value: '-maya' } });
    expect(screen.getByText(/Use up to 32 letters/)).toBeTruthy();
    expect(calls.some((c) => c.url === '/api/setup/owner')).toBe(false);
  });

  it('counts the characters still needed instead of a strength meter', async () => {
    render(Setup);
    await screen.findByText('Create your account');
    expect(screen.getByText('At least 12 characters.')).toBeTruthy();
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'abcdefghijk' } });
    expect(screen.getByText('1 more character needed.')).toBeTruthy();
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'abcdefgh' } });
    expect(screen.getByText('4 more characters needed.')).toBeTruthy();
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'abcdefghijkl' } });
    expect(screen.getByText('Long enough.')).toBeTruthy();
    const show = screen.getByRole('button', { name: 'Show' });
    expect(show.getAttribute('aria-pressed')).toBe('false');
    await fireEvent.click(show);
    expect((screen.getByLabelText('Password') as HTMLInputElement).type).toBe('text');
    expect(screen.getByRole('button', { name: 'Hide' }).getAttribute('aria-pressed')).toBe('true');
  });

  it('creates the owner with a name, then turns on team mode and the time zone silently', async () => {
    render(Setup);
    await fillAccount();
    await submitAccount();
    expect(await screen.findByText('Connect an AI model')).toBeTruthy();
    const owner = JSON.parse(calls.find((c) => c.url === '/api/setup/owner')!.body);
    expect(owner).toMatchObject({ display_name: 'Maya Chen', username: 'maya', password: 'Granite-River-42-Lantern' });
    expect(owner.timezone).toBeTruthy();
    const patch = JSON.parse(calls.find((c) => c.method === 'PATCH' && c.url === '/api/config')!.body);
    expect(patch.set).toEqual({ mode: 'multi', timezone: owner.timezone });
    expect(session.me?.user.display_name).toBe('Maya Chen');
  });

  it('puts a common password back on the password field', async () => {
    render(Setup);
    await fillAccount('Maya', 'maya', 'password1234');
    await submitAccount();
    expect(await screen.findByText('This password is too common. Try a phrase of a few words.')).toBeTruthy();
    expect(screen.getByLabelText('Password').getAttribute('aria-invalid')).toBe('true');
  });

  it('sends a timed-out setup back to the code and keeps what was typed', async () => {
    withMock('grant-expired');
    await session.init();
    history.replaceState(null, '', '/setup#t=demo-setup-token');
    render(Setup);
    await fillAccount();
    await submitAccount();
    expect(await screen.findByText('Setup timed out. Paste the setup code again.')).toBeTruthy();
    expect(screen.getByLabelText('Setup code')).toBeTruthy();
  });

  it('says when Bothy does not answer', async () => {
    calls = installApi({ setup: true, override: (_m, url) => (url === '/api/setup/owner' ? new Response('bad gateway', { status: 502 }) : undefined) });
    await session.init();
    history.replaceState(null, '', '/setup#t=demo-setup-token');
    render(Setup);
    await fillAccount();
    await submitAccount();
    expect((await screen.findByRole('alert')).textContent).toBe("Bothy didn't respond. Check that it's still running, then try again.");
  });
});

describe('steps 2 and 3', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = withMock('detect-aws');
    session.me = null;
    session.finishing = false;
    await session.init();
    history.replaceState(null, '', '/setup#t=demo-setup-token');
  });

  it('writes nothing while checking, saves once on Continue, then completes setup on the last step', async () => {
    render(Setup);
    await fillAccount();
    await submitAccount();
    await screen.findByText('Connect an AI model');
    const before = writes(calls).length;
    expect(await screen.findByText('Connected. Bothy will use Claude Sonnet 4.5.', {}, { timeout: 3000 })).toBeTruthy();
    expect(writes(calls).length).toBe(before);

    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    expect(await screen.findByText('Bothy is ready')).toBeTruthy();
    const save = writes(calls).slice(before);
    expect(save.map((c) => `${c.method} ${c.url}`)).toEqual(['POST /api/providers/connect']);
    const req = JSON.parse(save[0].body);
    expect(req).toMatchObject({
      id: 'bedrock',
      kind: 'bedrock',
      region: 'us-east-1',
      model: 'us.anthropic.claude-sonnet-4-5-20250929-v1:0',
      quick_model: 'us.anthropic.claude-haiku-4-5-20251001-v1:0',
    });

    await waitFor(() => expect(session.finishing).toBe(true));
    expect(calls.some((c) => c.url === '/api/setup/complete')).toBe(true);
    expect(session.setup?.needs_setup).toBe(false);
    expect(calls.some((c) => c.url === '/api/security-audit')).toBe(true);
    expect(screen.queryByText(/security/i)).toBeNull();

    await fireEvent.click(screen.getByRole('button', { name: 'Start chatting' }));
    expect(location.pathname).toBe('/chat');
    expect(session.finishing).toBe(false);
  });

  it('opens the People page with its add dialog from "Add people"', async () => {
    render(Setup);
    await fillAccount();
    await submitAccount();
    await screen.findByText('Connected. Bothy will use Claude Sonnet 4.5.', {}, { timeout: 3000 });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    await screen.findByText('Bothy is ready');
    // Link rows, so "Start chatting" is the one button on the screen.
    expect(screen.getAllByRole('button').map((b) => b.textContent?.trim())).toEqual(['Start chatting']);
    const add = screen.getByRole('link', { name: 'Add people' });
    expect(add.getAttribute('aria-describedby') && document.getElementById(add.getAttribute('aria-describedby')!)?.textContent).toBe('Give each person their own sign-in to Bothy.');
    await fireEvent.click(add);
    await waitFor(() => expect(location.pathname + location.search).toBe('/settings/people?add=1'));
  });
});
