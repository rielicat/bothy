import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { resetMock } from '../../mock/server';
import { setCsrf } from '../lib/api/client';
import { installApi, type Call } from '../test/api';
import ProviderChooser from './ProviderChooser.svelte';

function withMock(mock = ''): Call[] {
  const calls = installApi();
  resetMock({ mock });
  setCsrf('mock-csrf');
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET' && /\/api\/(config|vault)/.test(c.url));
const tests = (calls: Call[]) => calls.filter((c) => c.url === '/api/providers/test').map((c) => JSON.parse(c.body));
const radio = (name: string) => screen.getByRole('radio', { name: new RegExp(`^${name}`) });
const LONG_KEY = 'sk-ant-api03-0123456789abcdefghij';

async function paste(input: HTMLElement, value: string) {
  (input as HTMLInputElement).value = value;
  await fireEvent(input, new InputEvent('input', { bubbles: true, inputType: 'insertFromPaste' }));
}

describe('ProviderChooser in setup', () => {
  beforeEach(() => vi.useRealTimers());

  it('pre-selects a single detected AWS option and checks it at once', async () => {
    const calls = withMock('detect-aws');
    render(ProviderChooser, { onconnected: vi.fn() });
    expect(await screen.findByText('Found on this server')).toBeTruthy();
    expect(screen.getByText('Or choose a provider')).toBeTruthy();
    await waitFor(() => expect(radio("Use this server's AWS access")).toHaveProperty('checked', true));
    expect(screen.getByText('Amazon Bedrock in US East (N. Virginia). No keys needed.')).toBeTruthy();
    // Bedrock is offered as a provider only when it wasn't found.
    expect(screen.queryByRole('radio', { name: /^Amazon Bedrock/ })).toBeNull();
    expect(screen.getByText('Checking AWS access…')).toBeTruthy();
    expect(await screen.findByText(/^Connected\. Bothy will use Claude\ Sonnet\ 4\.5\./, {}, { timeout: 3000 })).toBeTruthy();
    expect(tests(calls)[0]).toMatchObject({ kind: 'bedrock', region: 'us-east-1' });
    expect(writes(calls)).toEqual([]);
  });

  it('re-checks on a region change and lists models by friendly name behind Change', async () => {
    const calls = withMock('detect-aws');
    render(ProviderChooser);
    await screen.findByText(/^Connected\. Bothy will use Claude\ Sonnet\ 4\.5\./, {}, { timeout: 3000 });
    await fireEvent.change(screen.getByLabelText('Region'), { target: { value: 'eu-west-1' } });
    expect(screen.getByText('Checking which models you can use…')).toBeTruthy();
    await screen.findByText(/^Connected\. Bothy will use Claude\ Sonnet\ 4\.5\./, {}, { timeout: 3000 });
    expect(tests(calls).at(-1)).toMatchObject({ region: 'eu-west-1' });

    await fireEvent.click(screen.getByRole('button', { name: 'Change' }));
    const select = screen.getByLabelText('Model') as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(['Claude Sonnet 4.5 (recommended)', 'Claude Haiku 4.5', 'Nova Pro']);
    expect(screen.queryByText(/Connected\. Bothy will use/)).toBeNull();
  });

  it('offers the provider list while detection runs and keeps a choice made meanwhile', async () => {
    const calls = withMock('detect-aws');
    render(ProviderChooser);
    // Detection answers after 600 ms in the mock; the list is there before that.
    await fireEvent.click(radio('Amazon Bedrock'));
    expect(await screen.findByText('Found on this server', {}, { timeout: 3000 })).toBeTruthy();
    await new Promise((r) => setTimeout(r, 50));
    expect(radio('Amazon Bedrock')).toHaveProperty('checked', true);
    expect(radio("Use this server's AWS access")).toHaveProperty('checked', false);
    expect(tests(calls)).toEqual([]);
  });

  it('links a failure to the field that caused it', async () => {
    withMock('detect-none,provider-fail:key_rejected');
    render(ProviderChooser);
    await fireEvent.click(radio('Anthropic'));
    const key = screen.getByLabelText('API key');
    await paste(key, LONG_KEY);
    const line = await screen.findByText(/^That key wasn't accepted\./, {}, { timeout: 3000 });
    expect(key.getAttribute('aria-invalid')).toBe('true');
    expect(key.getAttribute('aria-describedby')).toBe(line.closest('p')!.id);
  });

  it('shows the install command for an empty Ollama in code type', async () => {
    withMock('ollama-empty');
    render(ProviderChooser);
    const code = await screen.findByText('ollama pull qwen3:8b', { selector: 'code' }, { timeout: 3000 });
    expect(code.closest('p')!.textContent).toMatch(/Install one on the server, for example ollama pull qwen3:8b, then check again\./);
  });

  it('shows "Choose a provider" when nothing was found and keeps Continue disabled with its reason', async () => {
    withMock('detect-none');
    render(ProviderChooser);
    expect(await screen.findByText('Choose a provider')).toBeTruthy();
    expect(screen.queryByText('Found on this server')).toBeNull();
    const cont = screen.getByRole('button', { name: 'Continue' });
    expect(cont.getAttribute('aria-disabled')).toBe('true');
    const reason = screen.getByText('Connect a model to continue.');
    expect(cont.getAttribute('aria-describedby')).toBe(reason.id);
    await fireEvent.click(cont);
    expect(document.activeElement).toBe(radio('Anthropic'));
  });

  it('checks a pasted key at once, a typed one after a pause, and saves only on Continue', async () => {
    const onconnected = vi.fn();
    const calls = withMock('detect-none');
    render(ProviderChooser, { onconnected });
    await fireEvent.click(await screen.findByRole('radio', { name: /^Anthropic/ }));
    expect(screen.getByText(/Stored encrypted\. Only sent to Anthropic\./)).toBeTruthy();
    expect(screen.getByRole('link', { name: /Get a key at console\.anthropic\.com/ }).getAttribute('target')).toBe('_blank');
    const key = screen.getByLabelText('API key');
    expect((key as HTMLInputElement).type).toBe('password');

    await paste(key, LONG_KEY);
    expect(screen.getByText('Checking the key…')).toBeTruthy();
    expect(await screen.findByText(/^Connected\. Bothy will use Claude\ Sonnet\ 4\.6\./, {}, { timeout: 3000 })).toBeTruthy();
    expect(tests(calls)).toEqual([expect.objectContaining({ kind: 'anthropic', api_key: LONG_KEY, model: 'claude-sonnet-4-6' })]);
    expect(writes(calls)).toEqual([]);

    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    await waitFor(() => expect(onconnected).toHaveBeenCalled());
    expect(writes(calls).map((c) => `${c.method} ${c.url}`)).toEqual(['PUT /api/vault/global/anthropic_api_key', 'PATCH /api/config']);
    const set = JSON.parse(writes(calls)[1].body).set;
    expect(set).toEqual({
      'providers.anthropic': { id: 'anthropic', type: 'anthropic', base_url: '', api_key: 'secret:anthropic_api_key', allow_private: false, discover: false },
      'models.main': ['anthropic/claude-sonnet-4-6'],
      'models.fast': ['anthropic/claude-haiku-4-5-20251001'],
    });
  });

  it('waits for a pause before checking typed keys, and a new value cancels the old check', async () => {
    const calls = withMock('detect-none');
    render(ProviderChooser);
    await fireEvent.click(await screen.findByRole('radio', { name: /^OpenAI/ }));
    const key = screen.getByLabelText('API key');
    vi.useFakeTimers();
    await fireEvent.input(key, { target: { value: 'sk-short' } });
    await vi.advanceTimersByTimeAsync(1000);
    expect(tests(calls)).toHaveLength(0);
    await fireEvent.input(key, { target: { value: 'sk-proj-0123456789abcdefghij' } });
    await vi.advanceTimersByTimeAsync(700);
    expect(tests(calls)).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(200);
    expect(tests(calls)).toHaveLength(1);
    vi.useRealTimers();
  });

  it('shows each failure as a plain sentence with its raw message behind Details', async () => {
    withMock('detect-none,provider-fail:out_of_credit');
    render(ProviderChooser);
    await fireEvent.click(await screen.findByRole('radio', { name: /^Anthropic/ }));
    await paste(screen.getByLabelText('API key'), LONG_KEY);
    expect(await screen.findByText(/Your Anthropic account is out of credit/, {}, { timeout: 3000 })).toBeTruthy();
    expect(screen.queryByText(/credit balance is too low/)).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Details' }));
    expect(screen.getByText(/credit balance is too low/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Continue' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('blocks a typed private address until it is allowed, then re-checks with that permission', async () => {
    const calls = withMock('detect-none');
    render(ProviderChooser);
    await screen.findByText('Choose a provider');
    await fireEvent.click(screen.getByRole('button', { name: 'More providers' }));
    await fireEvent.click(radio('Other server'));
    const address = screen.getByLabelText('Server address');
    expect(address.getAttribute('placeholder')).toBe('https://models.example.com/v1');
    await fireEvent.input(address, { target: { value: 'http://127.0.0.1:9/v1' } });
    await fireEvent.keyDown(address, { key: 'Enter' });
    expect(await screen.findByText('Bothy blocks addresses on your private network unless you allow them.', {}, { timeout: 3000 })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Allow 127.0.0.1:9' }));
    expect(await screen.findByText(/^Connected\. Bothy will use/, {}, { timeout: 3000 })).toBeTruthy();
    expect(tests(calls).at(-1)).toMatchObject({ base_url: 'http://127.0.0.1:9/v1', allow_private: true });
  });

  it('asks for a model by name when the server lists none', async () => {
    const calls = withMock('detect-none,no-model-list');
    render(ProviderChooser);
    await screen.findByText('Choose a provider');
    await fireEvent.click(screen.getByRole('button', { name: 'More providers' }));
    await fireEvent.click(radio('Other server'));
    const address = screen.getByLabelText('Server address');
    await fireEvent.input(address, { target: { value: 'https://models.example.com/v1' } });
    await fireEvent.keyDown(address, { key: 'Enter' });
    const name = await screen.findByLabelText('Model name', {}, { timeout: 3000 });
    await fireEvent.input(name, { target: { value: 'llama-3.3-70b' } });
    await fireEvent.keyDown(name, { key: 'Enter' });
    expect(await screen.findByText(/^Connected\. Bothy will use Llama\ 3\.3\ 70B\./, {}, { timeout: 3000 })).toBeTruthy();
    expect(tests(calls).at(-1)).toMatchObject({ model: 'llama-3.3-70b' });
  });

  it('describes detected local servers and checks an environment key in place', async () => {
    const calls = withMock('');
    render(ProviderChooser);
    expect(await screen.findByText('3 models ready, including Qwen3 14B.')).toBeTruthy();
    expect(screen.getByText("Found in the server's settings. Choosing it saves a copy in Bothy's saved keys.")).toBeTruthy();
    // The one local server is chosen and checked; a key in the environment never is.
    await waitFor(() => expect(radio('Ollama on this computer')).toHaveProperty('checked', true));
    expect(radio('Use the Anthropic key on this server')).toHaveProperty('checked', false);
    await screen.findByText(/^Connected\./, {}, { timeout: 3000 });
    await fireEvent.click(radio('Use the Anthropic key on this server'));
    await waitFor(() => expect(tests(calls).at(-1)).toMatchObject({ kind: 'anthropic', env_var: 'ANTHROPIC_API_KEY' }));
    await screen.findByText(/^Connected\. Bothy will use Claude Sonnet 4\.6\./, {}, { timeout: 3000 });
    expect(tests(calls).at(-1).api_key).toBeUndefined();
  });

  it('notes what a small local model can do', async () => {
    withMock('detect-ollama,model-context:12000');
    render(ProviderChooser);
    await screen.findByText(/^Connected\. Bothy will use Qwen3 14B\./, {}, { timeout: 3000 });
    expect(screen.getByText(/This model can only keep short conversations in mind/)).toBeTruthy();
    expect(screen.queryByText(/12,?000|12K|context/i)).toBeNull();
  });

  it('shows the save failure under Continue', async () => {
    installApi({ override: (m, url) => (m === 'PATCH' && url === '/api/config' ? new Response('{"error":{"code":"internal","message":"disk full"}}', { status: 500 }) : undefined) });
    resetMock({ mock: 'detect-aws' });
    setCsrf('mock-csrf');
    render(ProviderChooser);
    await screen.findByText(/^Connected\./, {}, { timeout: 3000 });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toMatch(/^Bothy couldn't save this connection\. Try again\./);
  });
});

describe('ProviderChooser in settings', () => {
  it('saves the provider on the first successful check, without touching the model slots', async () => {
    const onconnected = vi.fn();
    const calls = withMock('detect-none');
    render(ProviderChooser, { mode: 'settings', onconnected });
    await fireEvent.click(await screen.findByRole('radio', { name: /^Google Gemini/ }));
    await paste(screen.getByLabelText('API key'), 'AIzaSy0123456789abcdefghijk');
    await waitFor(() => expect(onconnected).toHaveBeenCalledWith(expect.objectContaining({ id: 'gemini', name: 'Google Gemini' })), { timeout: 3000 });
    expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull();
    const set = JSON.parse(writes(calls).at(-1)!.body).set;
    expect(Object.keys(set)).toEqual(['providers.gemini']);
    expect(within(document.body).getByText('Connected.')).toBeTruthy();
  });
});
