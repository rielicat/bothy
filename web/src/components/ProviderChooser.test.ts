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

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET' && /\/api\/(config|vault|providers\/connect)/.test(c.url));
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

  it('keeps AWS settings whose sign-in expired, says how to fix them and checks only when asked', async () => {
    const calls = withMock('aws-expired');
    render(ProviderChooser);
    expect(await screen.findByText("This server's AWS sign-in has expired.")).toBeTruthy();
    expect(screen.getByText('Choose a provider', { exact: false })).toBeTruthy();
    await new Promise((r) => setTimeout(r, 50));
    expect(radio("Use this server's AWS access")).toHaveProperty('checked', false);
    await fireEvent.click(radio("Use this server's AWS access"));
    expect(screen.getByText('aws sso login')).toBeTruthy();
    expect(tests(calls)).toEqual([]);
    await fireEvent.click(screen.getByRole('button', { name: 'Check again' }));
    expect(await screen.findByText(/^Connected\. Bothy will use Claude\ Sonnet\ 4\.5\./, {}, { timeout: 3000 })).toBeTruthy();
    expect(tests(calls)).toEqual([expect.objectContaining({ kind: 'bedrock', region: 'us-east-1' })]);
    expect(screen.queryByText("This server's AWS sign-in has expired.")).toBeNull();
  });

  it('keeps Check again after a failed check of AWS settings without access', async () => {
    const calls = withMock('aws-expired,provider-fail:aws_no_credentials');
    render(ProviderChooser);
    await fireEvent.click(await screen.findByRole('radio', { name: /^Use this server's AWS access/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Check again' }));
    expect(await screen.findByText(/doesn't have AWS access right now/, {}, { timeout: 3000 })).toBeTruthy();
    expect(screen.queryByText(/restart Bothy/)).toBeNull();
    expect(screen.getByText("This server's AWS sign-in has expired.")).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Check again' }));
    await waitFor(() => expect(tests(calls)).toHaveLength(2), { timeout: 3000 });
  });

  it('says AWS settings give no access, and lists them after a working local server', async () => {
    const calls = withMock('aws-expired:no_credentials,detect-ollama');
    render(ProviderChooser);
    expect(await screen.findByText("AWS settings found, but they don't give access right now.")).toBeTruthy();
    const found = within(screen.getByRole('radiogroup', { name: 'Found on this server' }))
      .getAllByRole('radio')
      .map((r) => r.getAttribute('aria-label') ?? r.closest('label')?.textContent ?? '');
    expect(found.findIndex((t) => /Ollama/.test(t))).toBeLessThan(found.findIndex((t) => /AWS access/.test(t)));
    await waitFor(() => expect(radio('Ollama on this computer')).toHaveProperty('checked', true));
    await fireEvent.click(radio("Use this server's AWS access"));
    expect(screen.getByText(/Give this server an AWS role or sign in with the AWS CLI/)).toBeTruthy();
    expect(tests(calls).some((t) => t.kind === 'bedrock')).toBe(false);
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
    // No model named: the check tries the one Anthropic suggests and says which.
    expect(tests(calls)).toEqual([expect.objectContaining({ kind: 'anthropic', api_key: LONG_KEY })]);
    expect(tests(calls)[0].model).toBeUndefined();
    expect(writes(calls)).toEqual([]);

    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    await waitFor(() => expect(onconnected).toHaveBeenCalled());
    expect(writes(calls).map((c) => `${c.method} ${c.url}`)).toEqual(['POST /api/providers/connect']);
    expect(JSON.parse(writes(calls)[0].body)).toEqual({
      id: 'anthropic',
      kind: 'anthropic',
      base_url: '',
      allow_private: false,
      api_key: LONG_KEY,
      model: 'claude-sonnet-4-6',
      quick_model: 'claude-haiku-4-5-20251001',
    });
  });

  it('sends an AWS Secrets Manager reference as a reference, never as a key', async () => {
    const onconnected = vi.fn();
    const calls = withMock('detect-none');
    render(ProviderChooser, { onconnected });
    await fireEvent.click(await screen.findByRole('radio', { name: /^Anthropic/ }));
    const ref = 'aws-sm:prod/llm#ANTHROPIC_API_KEY';
    await paste(screen.getByLabelText('API key'), ref);
    expect(await screen.findByText(/^Connected\./, {}, { timeout: 3000 })).toBeTruthy();
    expect(tests(calls)[0]).toMatchObject({ kind: 'anthropic', secret_ref: ref });
    expect(tests(calls)[0].api_key).toBeUndefined();

    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    await waitFor(() => expect(onconnected).toHaveBeenCalled());
    const saved = JSON.parse(writes(calls)[0].body);
    expect(saved.secret_ref).toBe(ref);
    expect(saved.api_key).toBeUndefined();
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
    installApi({ override: (m, url) => (m === 'POST' && url === '/api/providers/connect' ? new Response('{"error":{"code":"internal","message":"disk full"}}', { status: 500 }) : undefined) });
    resetMock({ mock: 'detect-aws' });
    setCsrf('mock-csrf');
    render(ProviderChooser);
    await screen.findByText(/^Connected\./, {}, { timeout: 3000 });
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toMatch(/^Bothy couldn't save this connection\. Try again\./);
  });
});

describe('ProviderChooser with the server\'s model names (S5)', () => {
  it('names and suggests models the way the check says', async () => {
    const choices = [
      // The server's suggestion wins over the client's own pick (Sonnet).
      { id: 'claude-opus-4-1-20250805', display: 'claude-opus-4-1-20250805', display_name: 'Claude Opus 4.1 (team plan)', recommended: true },
      { id: 'claude-sonnet-4-6', display: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6' },
      { id: 'claude-haiku-4-5-20251001', display: 'claude-haiku-4-5-20251001', display_name: 'Claude Haiku 4.5', quick_tasks_default: true },
    ];
    const probe = { ok: true, latency: 1e9, tool_calling: true, usage_reported: true, context: 200000, models: choices.map((c) => c.id), cost_micros: 31, detail: '', choices };
    const calls = installApi({ override: (m, url) => (m === 'POST' && url === '/api/providers/test' ? probe : undefined) });
    resetMock({ mock: 'detect-none' });
    setCsrf('mock-csrf');
    const onconnected = vi.fn();
    render(ProviderChooser, { onconnected });
    await fireEvent.click(await screen.findByRole('radio', { name: /^Anthropic/ }));
    await paste(screen.getByLabelText('API key'), LONG_KEY);
    expect(await screen.findByText(/^Connected\. Bothy will use Claude Opus 4\.1 \(team plan\)\./, {}, { timeout: 3000 })).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    await waitFor(() => expect(onconnected).toHaveBeenCalled());
    expect(JSON.parse(writes(calls)[0].body)).toMatchObject({ model: 'claude-opus-4-1-20250805', quick_model: 'claude-haiku-4-5-20251001' });
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
    const req = JSON.parse(writes(calls).at(-1)!.body);
    expect(req).toMatchObject({ id: 'gemini', kind: 'gemini' });
    expect(req.model).toBeUndefined();
    expect(within(document.body).getByText('Connected.')).toBeTruthy();
  });
});
