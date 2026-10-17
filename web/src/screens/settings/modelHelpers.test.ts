import { describe, expect, it } from 'vitest';
import { models as fixtureModels } from '../../../mock/fixtures';
import {
  addBackup,
  failureText,
  makePrimary,
  modelName,
  modelProvider,
  moveBackup,
  providerName,
  providerStatus,
  refinePresetOf,
  regionName,
  removeAt,
  splitRef,
} from './modelHelpers';

describe('names', () => {
  it('names providers by id, then by kind, and keeps unknown ids readable', () => {
    expect(providerName('anthropic')).toBe('Anthropic');
    expect(providerName('gemini')).toBe('Google Gemini');
    expect(providerName('work', 'ollama')).toBe('Ollama');
    expect(providerName('vllm', 'openai_compat')).toBe('Vllm');
  });

  it('splits a model ref at the first slash only', () => {
    expect(splitRef('openrouter/anthropic/claude-sonnet-4.5')).toEqual({ provider: 'openrouter', model: 'anthropic/claude-sonnet-4.5' });
  });

  it('uses the friendly name, drops "(local)", and works one out from the id without one (S5)', () => {
    expect(modelName('anthropic/claude-sonnet-4-6', fixtureModels)).toBe('Claude Sonnet 4.6');
    expect(modelName('ollama/qwen3:14b', fixtureModels)).toBe('Qwen3 14B');
    // Never a raw tag next to named models.
    expect(modelName('ollama/qwen3:4b', fixtureModels)).toBe('Qwen3 4B');
    expect(modelName('x/y', [{ ...fixtureModels[0], ref: { provider: 'x', model: 'y' }, display_name: 'Why' }])).toBe('Why');
    expect(modelProvider('ollama/qwen3:4b', fixtureModels)).toBe('Ollama');
  });

  it('names Bedrock regions as the AWS console does', () => {
    expect(regionName('us-east-1')).toBe('US East (N. Virginia)');
    expect(regionName('xx-nowhere-9')).toBe('xx-nowhere-9');
  });
});

describe('model chains', () => {
  const chain = ['a/main', 'b/one', 'c/two'];

  it('replaces the model and keeps the backups', () => {
    expect(makePrimary(chain, 'd/new')).toEqual(['d/new', 'b/one', 'c/two']);
    expect(makePrimary(chain, 'c/two')).toEqual(['c/two', 'b/one']);
  });

  it('moves backups without ever passing the main model', () => {
    expect(moveBackup(chain, 2, -1)).toEqual(['a/main', 'c/two', 'b/one']);
    expect(moveBackup(chain, 1, -1)).toBe(chain);
    expect(moveBackup(chain, 2, 1)).toBe(chain);
  });

  it('adds a backup once and removes by position', () => {
    expect(addBackup(chain, 'b/one')).toBe(chain);
    expect(addBackup(chain, 'd/x')).toEqual([...chain, 'd/x']);
    expect(removeAt(chain, 1)).toEqual(['a/main', 'c/two']);
  });
});

describe('provider status', () => {
  const base = { kind: 'anthropic', enabled: true, models: 6 };

  it('says connected with the model count, Bedrock too', () => {
    expect(providerStatus(base)).toEqual({ text: 'Connected · 6 models', tone: 'muted' });
    expect(providerStatus({ ...base, models: 1 }).text).toBe('Connected · 1 model');
    // The region and where its access comes from are in Manage.
    expect(providerStatus({ ...base, kind: 'bedrock', region: 'eu-west-2', models: 3 }).text).toBe('Connected · 3 models');
  });

  it('colours only what needs attention', () => {
    const at = (state: string) => providerStatus({ ...base, health: { id: 'x', kind: 'anthropic', state, since: '' } });
    expect(at('down')).toEqual({ text: "Can't reach it right now", tone: 'danger' });
    expect(at('auth_failed').tone).toBe('danger');
    expect(at('degraded').tone).toBe('warning');
    expect(providerStatus({ ...base, enabled: false })).toEqual({ text: 'Off', tone: 'muted' });
  });
});

describe('failed checks', () => {
  it('says what happened and what to do, naming the provider', () => {
    expect(failureText('rate_limited', 'Anthropic')).toBe('Anthropic is limiting requests right now. Wait a minute.');
    expect(failureText('unreachable', 'Ollama', 'ollama')).toBe("Ollama isn't answering at that address. Make sure it's running.");
    expect(failureText('unreachable', 'OpenAI', 'openai')).toMatch(/^Bothy couldn't reach OpenAI from this server\./);
    expect(failureText('no_model_access', 'OpenAI', 'openai', 'GPT-5')).toBe("This key works, but it can't use GPT-5. Choose another model.");
  });
});

it('reads "How thorough" back from the refine numbers', () => {
  expect(refinePresetOf(3, 2)).toBe('standard');
  expect(refinePresetOf(5, 2)).toBe('thorough');
  expect(refinePresetOf(4, 1)).toBe('custom');
});
