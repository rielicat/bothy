import { beforeEach, describe, expect, it } from 'vitest';
import { installApi, type Call } from '../test/api';
import { setCsrf } from './api/client';
import { config, sourceLabel, type SchemaNode } from './config.svelte';
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

  it('renders the merged config as YAML with list items and source comments', () => {
    const y = toYaml(config.view!.entries, config.view!.schema as SchemaNode, (e) => e.source.layer);
    expect(y).toContain('providers:\n  - # anthropic\n    id: "anthropic"  # file\n');
    expect(y).toContain('  "ollama/qwen3:14b":\n    in: 0  # file\n');
    expect(y).toContain('policy:\n  preset: "balanced"  # ui\n');
  });
});
