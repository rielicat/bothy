import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { toaster } from '../../components/ui';
import { setCsrf } from '../../lib/api/client';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import Settings from './Settings.svelte';
import { settingsConfig } from './store.svelte';

const patches = (calls: Call[]) => calls.filter((c) => c.method === 'PATCH' && c.url === '/api/config').map((c) => JSON.parse(c.body).set);
const tests = (calls: Call[]) => calls.filter((c) => c.method === 'POST' && c.url === '/api/providers/test').map((c) => JSON.parse(c.body));

describe('Settings → Models', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    session.ready = false;
    await session.init();
    setCsrf('mock-csrf');
    settingsConfig.view = null;
    render(Settings, { params: { group: 'models', page: 'models' } });
    await screen.findByText('Main model');
    await screen.findByText(/Connected · 2 models/);
  });

  it('names models and providers, never by id', () => {
    expect(screen.getAllByText('Claude Sonnet 4.6').length).toBeGreaterThan(0);
    expect(screen.getByText("Can't reach it right now").className).toContain('text-danger-text');
    expect(document.body.textContent).not.toMatch(/claude-sonnet-4-6|anthropic\//);
  });

  it('changes the main model and keeps its backups', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Change main model' }));
    const dialog = await screen.findByRole('dialog', { name: 'Main model' });
    const list = within(dialog).getByRole('listbox', { name: 'Main model' });
    expect(within(list).getByRole('option', { name: 'Claude Sonnet 4.6' }).getAttribute('aria-selected')).toBe('true');
    expect(within(list).getAllByRole('group').map((g) => g.firstElementChild?.textContent)).toEqual(['Anthropic', 'Ollama']);
    await fireEvent.click(within(list).getByRole('option', { name: 'Claude Haiku 4.5' }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patches(calls)).toEqual([{ 'models.main': ['anthropic/claude-haiku-4-5', 'ollama/qwen3:14b'] }]));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Main model' })).toBeNull());
  });

  it('chooses a model from the keyboard', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Change quick tasks model' }));
    const dialog = await screen.findByRole('dialog', { name: 'Quick tasks model' });
    const list = within(dialog).getByRole('listbox');
    await fireEvent.keyDown(list, { key: 'Home' });
    expect(list.getAttribute('aria-activedescendant')).toBe(within(list).getAllByRole('option')[0].id);
    await fireEvent.keyDown(list, { key: 'Enter' });
    await waitFor(() => expect(patches(calls)).toEqual([{ 'models.fast': ['anthropic/claude-sonnet-4-6', 'ollama/qwen3:4b'] }]));
  });

  it('reorders and adds backup models', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Backup models' }));
    expect(await screen.findByText('If a model doesn\'t respond, Bothy tries these in order.')).toBeTruthy();
    const add = screen.getByLabelText('Add a backup for the main model') as HTMLSelectElement;
    await fireEvent.change(add, { target: { value: 'anthropic/claude-haiku-4-5' } });
    await waitFor(() => expect(patches(calls)).toEqual([{ 'models.main': ['anthropic/claude-sonnet-4-6', 'ollama/qwen3:14b', 'anthropic/claude-haiku-4-5'] }]));
    await fireEvent.click(await screen.findByRole('button', { name: 'Options for Claude Haiku 4.5' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Move up' }));
    await waitFor(() => expect(patches(calls).at(-1)).toEqual({ 'models.main': ['anthropic/claude-sonnet-4-6', 'anthropic/claude-haiku-4-5', 'ollama/qwen3:14b'] }));
  });

  // A provider set to one wire API must be checked over that API, not the default for its type.
  it('checks a provider over its configured api when Manage opens', async () => {
    await settingsConfig.save({ 'providers.openai.api': 'chat', 'providers.openai.enabled': true });
    await fireEvent.click(await screen.findByRole('button', { name: 'Manage OpenAI' }));
    await waitFor(() => expect(tests(calls).at(-1)).toMatchObject({ kind: 'openai', api: 'chat', secret_ref: 'secret:openai_api_key' }));
    const dialog = screen.getByRole('dialog', { name: 'OpenAI' });
    expect(await within(dialog).findByText(/^Connected\./)).toBeTruthy();
  });

  it('replaces a key: checks it, stores it, and starts no second check', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Manage Anthropic' }));
    const dialog = await screen.findByRole('dialog', { name: 'Anthropic' });
    await within(dialog).findByText(/^Connected\./, undefined, { timeout: 3000 });
    const checks = tests(calls).length;
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Replace' }));
    const key = within(dialog).getByLabelText('API key') as HTMLInputElement;
    await fireEvent.input(key, { target: { value: 'sk-ant-0123456789abcdefghijkl' } });
    await fireEvent.keyDown(key, { key: 'Enter' });
    await within(dialog).findByText('Connected. Bothy uses the new key from now on.', undefined, { timeout: 3000 });
    expect(calls.some((c) => c.method === 'PUT' && c.url === '/api/vault/global/anthropic_api_key')).toBe(true);
    await new Promise((r) => setTimeout(r, 100));
    expect(tests(calls).length).toBe(checks + 1);
    expect(key.value).toBe('sk-ant-0123456789abcdefghijkl');
  });

  it('says where a provider gets its credentials, never what they are', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Manage Anthropic' }));
    const dialog = await screen.findByRole('dialog', { name: 'Anthropic' });
    expect(within(dialog).getByText('Credentials from: Saved key')).toBeTruthy();
    expect(dialog.textContent).not.toMatch(/sk-ant|secret:/);
  });

  it('turns an off provider on from Manage', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Manage Google Gemini' }));
    const dialog = await screen.findByRole('dialog', { name: 'Google Gemini' });
    expect(within(dialog).getByText(/Off\. Bothy doesn't use this provider\./)).toBeTruthy();
    expect(tests(calls)).toEqual([]);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Turn on' }));
    await waitFor(() => expect(patches(calls)).toEqual([{ 'providers.gemini.enabled': true }]));
    await waitFor(() => expect(tests(calls).length).toBe(1));
  });

  it("won't remove the provider the main model uses", async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Manage Anthropic' }));
    const dialog = await screen.findByRole('dialog', { name: 'Anthropic' });
    const remove = within(dialog).getByRole('button', { name: 'Remove provider…' });
    expect(remove.getAttribute('aria-disabled')).toBe('true');
    expect(within(dialog).getByText('Bothy uses Anthropic for the main model. Choose another main model first.')).toBeTruthy();
  });

  it('removes a provider and its models from the backups', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Manage Ollama' }));
    const dialog = await screen.findByRole('dialog', { name: 'Ollama' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Remove provider…' }));
    const confirm = await screen.findByRole('dialog', { name: 'Remove Ollama?' });
    await fireEvent.click(within(confirm).getByRole('button', { name: 'Remove provider' }));
    await waitFor(() =>
      expect(patches(calls)).toEqual([{ 'providers.ollama': null, 'models.main': ['anthropic/claude-sonnet-4-6'], 'models.fast': ['anthropic/claude-haiku-4-5'] }]),
    );
  });

  it('asks how thorough only while Refine is on', async () => {
    expect(await screen.findByText(/costs about 12× more\./)).toBeTruthy();
    expect(screen.queryByLabelText('How thorough')).toBeNull();
    await fireEvent.click(screen.getByRole('switch', { name: 'Refine answers by default' }));
    await waitFor(() => expect(patches(calls)).toEqual([{ 'agent.refine.enabled': true }]));
    const how = (await screen.findByLabelText('How thorough')) as HTMLSelectElement;
    await fireEvent.change(how, { target: { value: 'thorough' } });
    await waitFor(() => expect(patches(calls).at(-1)).toEqual({ 'agent.refine.drafts': 5, 'agent.refine.depth': 2 }));
    expect(await screen.findByText(/costs about 20× more\./)).toBeTruthy();
  });

  it('adds a provider with a pasted key and says so', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Add a provider' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add a provider' });
    expect(within(dialog).queryByRole('radio', { name: /Anthropic/ })).toBeNull();
    await fireEvent.click(within(dialog).getByText('More providers'));
    await fireEvent.click(within(dialog).getByRole('radio', { name: /Groq/ }));
    const key = within(dialog).getByLabelText('API key');
    await fireEvent.input(key, { target: { value: 'gsk_0123456789abcdefghij' } });
    await fireEvent.keyDown(key, { key: 'Enter' });
    const connect = () => calls.find((c) => c.method === 'POST' && c.url === '/api/providers/connect');
    await waitFor(() => expect(connect()).toBeTruthy(), { timeout: 3000 });
    // Settings adds the provider only; the model slots stay as they are.
    expect(JSON.parse(connect()!.body)).toEqual({ id: 'groq', kind: 'openai_compat', base_url: 'https://api.groq.com/openai/v1', allow_private: false, api_key: 'gsk_0123456789abcdefghij' });
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add a provider' })).toBeNull());
    await waitFor(() => expect(toaster.current?.text).toBe('Groq connected.'));
  });
});
