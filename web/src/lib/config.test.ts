import { beforeEach, describe, expect, it } from 'vitest';
import { installApi, type Call } from '../test/api';
import { setCsrf } from './api/client';
import { config, saveErrorText, sourceLabel, type SchemaNode } from './config.svelte';
import { toYaml } from './yaml';

// The fixtures come from the real internal/config package (effective view of
// its testdata/bothy.yaml plus a UI and an env override).
describe('config store', () => {
  let calls: Call[];

  beforeEach(async () => {
    calls = installApi();
    setCsrf('mock-csrf');
    config.view = null;
    await config.load();
  });

  it('indexes entries by path and lists keys of each kind', () => {
    expect(config.value('policy.preset')).toBe('balanced');
    expect(config.flag('providers.ollama.allow_private')).toBe(true);
    expect(config.keys('providers')).toEqual(['anthropic', 'openai', 'gemini', 'ollama', 'openrouter']);
    expect(config.keys('pricing', 'tail')).toEqual(['ollama/qwen3:14b']);
    expect(config.keys('providers.ollama.models', 'tail')).toEqual(['qwen3:14b']);
    expect(config.keys('mcp.servers.notes.side_effects', 'leaf')).toEqual(['default', 'list_*', 'read_*']);
  });

  it('walks the schema through list items and maps', () => {
    expect(config.schema('policy.preset')?.enum).toEqual(['careful', 'balanced', 'hands_off']);
    expect(config.schema('providers.ollama.base_url')?.type).toBe('string');
    expect(config.schema('budgets.run.max_time')?.format).toBe('duration');
    expect(config.schema('pricing.ollama/qwen3:14b.in')?.type).toBe('number');
  });

  it('sends changes as a dotted-path patch and reloads', async () => {
    expect(await config.set({ 'budgets.daily_usd': 7 }, true)).toBeNull();
    expect(calls.find((c) => c.method === 'PATCH')?.body).toBe('{"set":{"budgets.daily_usd":7}}');
    expect(config.value('budgets.daily_usd')).toBe(7);
    expect(config.entry('budgets.daily_usd')?.source.layer).toBe('ui');
  });

  it('keeps field errors from a 400 next to the path', async () => {
    const errs = await config.set({ 'budgets.daily_usd': -1 }, true);
    expect(errs?.[0].path).toBe('budgets.daily_usd');
    expect(config.errors['budgets.daily_usd']).toMatch(/must be 0 or more/);
  });

  it('resets a UI override to the next layer', async () => {
    await config.reset('policy.preset');
    expect(calls.at(-2)?.method).toBe('DELETE');
    expect(calls.at(-2)?.url).toBe('/api/config/policy.preset');
    expect(config.value('policy.preset')).toBe('careful');
  });

  it('labels every source layer', () => {
    expect(sourceLabel({ layer: 'file', file: '/data/bothy.yaml', line: 61 })).toBe('file · bothy.yaml:61');
    expect(sourceLabel({ layer: 'env', env_var: 'BOTHY_BUDGETS__DAILY_USD' })).toBe('env · BOTHY_BUDGETS__DAILY_USD');
    expect(sourceLabel({ layer: 'ui', by: 'user:usr_1' }, 'usr_1')).toBe('UI · you');
    expect(sourceLabel({ layer: 'default' })).toBe('default');
  });

  it('saves without toasts and keeps field errors by path', async () => {
    expect(await config.save({ 'budgets.daily_usd': 6 })).toEqual({ ok: true, restart: [] });
    const r = await config.save({ 'budgets.daily_usd': -1 });
    expect(r.ok).toBe(false);
    expect(!r.ok && r.errors['budgets.daily_usd']).toMatch(/must be 0 or more/);
    expect(saveErrorText(r, 'budgets.daily_usd')).toBe("Couldn't save. Try again.");
    expect(saveErrorText(r, 'budgets.daily_usd', { technical: true })).toBe("Couldn't save. Must be 0 or more.");
    expect(saveErrorText({ ok: false, errors: {}, message: '', status: 403 })).toBe("You can't do this. Ask an admin.");
    expect(saveErrorText({ ok: false, errors: {}, message: '', status: 409 })).toBe('Someone else changed this setting. Reload to see their change.');
  });

  it('knows the default under any layer and whether a value differs from it', () => {
    expect(config.defaultValue('budgets.daily_usd')).toBe(5);
    expect(config.changed('budgets.daily_usd')).toBe(true);
    expect(config.defaultValue('policy.preset')).toBe('careful');
    expect(config.changed('policy.preset')).toBe(true);
    // From bothy.yaml but equal to the default: not a change.
    expect(config.changed('mode')).toBe(false);
    expect(config.changed('no.such.path')).toBe(false);
    expect(config.defaultValue('sandbox.idle_timeout')).toBe('10m0s');
  });

  it('resets to the default: drops a UI override over the default, writes the default over anything else', async () => {
    // Under the UI value sits bothy.yaml's "careful", which is the default, so the override goes.
    calls.length = 0;
    await config.resetToDefault('policy.preset');
    expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/config/policy.preset')).toBe(true);
    expect(config.value('policy.preset')).toBe('careful');
    // budgets.daily_usd: UI 3 over env 4, so the default is written instead.
    calls.length = 0;
    await config.resetToDefault('budgets.daily_usd');
    expect(calls.find((c) => c.method === 'PATCH')?.body).toBe('{"set":{"budgets.daily_usd":5}}');
    expect(config.changed('budgets.daily_usd')).toBe(false);
  });

  it('renders the merged config as YAML with list items and source comments', () => {
    const y = toYaml(config.view!.entries, config.view!.schema as SchemaNode, (e) => e.source.layer);
    expect(y).toContain('providers:\n  - # anthropic\n    id: "anthropic"  # file\n');
    expect(y).toContain('  "ollama/qwen3:14b":\n    in: 0  # file\n');
    expect(y).toContain('policy:\n  preset: "balanced"  # ui\n');
  });
});
