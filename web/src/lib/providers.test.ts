import { describe, expect, it } from 'vitest';
import { bestModel, friendlyModel, hostPort, modelNote, modelOptions, pickModels, provider, providers, regionName } from './providers';

describe('friendly model names (S5 fallback)', () => {
  it.each([
    ['claude-sonnet-4-6', 'Claude Sonnet 4.6'],
    ['claude-haiku-4-5-20251001', 'Claude Haiku 4.5'],
    ['claude-opus-4', 'Claude Opus 4'],
    ['claude-3-5-sonnet-20241022', 'Claude 3.5 Sonnet'],
    ['us.anthropic.claude-sonnet-4-5-20250929-v1:0', 'Claude Sonnet 4.5'],
    ['us.amazon.nova-pro-v1:0', 'Nova Pro'],
    ['anthropic/claude-sonnet-4.5', 'Claude Sonnet 4.5'],
    ['gpt-5.1', 'GPT-5.1'],
    ['gpt-4o-mini', 'GPT-4o Mini'],
    ['o3-mini', 'o3 Mini'],
    ['gemini-2.5-pro', 'Gemini 2.5 Pro'],
    ['qwen3:14b', 'Qwen3 14B'],
    ['llama3.2:latest', 'Llama3.2'],
    ['llama-3.3-70b-versatile', 'Llama 3.3 70B Versatile'],
    ['deepseek-chat', 'DeepSeek Chat'],
    ['grok-4', 'Grok 4'],
  ])('%s → %s', (id, name) => expect(friendlyModel(id)).toBe(name));
});

describe('model choice', () => {
  it('picks the largest local model and skips embedding models', () => {
    expect(bestModel(['qwen3:4b', 'qwen3:14b', 'nomic-embed-text:137m', 'llama3.2:3b'])).toBe('qwen3:14b');
    expect(bestModel(['mistral', 'phi'])).toBe('mistral');
    expect(bestModel([])).toBe('');
  });

  it('recommends the provider default and a cheaper quick-tasks model when available', () => {
    expect(pickModels(provider('anthropic'), ['claude-opus-4-1', 'claude-sonnet-4-6', 'claude-haiku-4-5-20251001'])).toEqual({ main: 'claude-sonnet-4-6', quick: 'claude-haiku-4-5-20251001' });
    expect(pickModels(provider('anthropic'), [])).toEqual({ main: 'claude-sonnet-4-6', quick: 'claude-haiku-4-5' });
    expect(pickModels(provider('groq'), ['llama-3.3-70b-versatile'])).toEqual({ main: 'llama-3.3-70b-versatile', quick: 'llama-3.3-70b-versatile' });
    expect(pickModels(provider('ollama'), ['qwen3:4b', 'qwen3:14b'])).toEqual({ main: 'qwen3:14b', quick: 'qwen3:14b' });
    const bedrock = pickModels(provider('bedrock'), ['us.amazon.nova-pro-v1:0', 'us.anthropic.claude-haiku-4-5-v1:0', 'us.anthropic.claude-sonnet-4-5-v1:0']);
    expect(bedrock).toEqual({ main: 'us.anthropic.claude-sonnet-4-5-v1:0', quick: 'us.anthropic.claude-haiku-4-5-v1:0' });
  });

  it('lists models by friendly name, recommended first, without duplicate names', () => {
    expect(modelOptions(['claude-haiku-4-5', 'claude-haiku-4-5-20251001', 'claude-sonnet-4-6'], 'claude-sonnet-4-6')).toEqual([
      { value: 'claude-sonnet-4-6', label: 'Claude Sonnet 4.6 (recommended)' },
      { value: 'claude-haiku-4-5', label: 'Claude Haiku 4.5' },
    ]);
  });

  it('leads a long router list with one suggested model per family, then all models by name', () => {
    const ids = [
      '~unbiased/pareto-latest',
      'anthropic/claude-sonnet-4.5',
      'anthropic/claude-sonnet-5.5',
      'anthropic/claude-haiku-5.5',
      'anthropic/claude-opus-5.5',
      'openai/gpt-6.1-sol',
      'openai/gpt-6.1-sol-pro',
      'openai/gpt-6-mini',
      'google/gemini-3.1-pro-preview',
      'google/gemini-3-pro',
      'google/gemini-3.8-flash',
      'google/gemini-3.5-flash-lite',
      'google/gemini-3-pro-image',
      'x-ai/grok-4.7',
    ];
    const opts = modelOptions(ids, 'anthropic/claude-sonnet-5.5', (m) => m);
    expect(opts.filter((o) => o.group === 'Suggested').map((o) => o.value)).toEqual([
      'anthropic/claude-sonnet-5.5',
      'anthropic/claude-opus-5.5',
      'anthropic/claude-haiku-5.5',
      'openai/gpt-6.1-sol',
      'openai/gpt-6-mini',
      'google/gemini-3-pro',
      'google/gemini-3.8-flash',
      'x-ai/grok-4.7',
    ]);
    const all = opts.filter((o) => o.group === 'All models').map((o) => o.value);
    expect(all.at(0)).toBe('anthropic/claude-sonnet-4.5');
    expect(all.at(-1)).toBe('~unbiased/pareto-latest');
    expect(opts).toHaveLength(ids.length);
  });
});

describe('model notes (§5.6.3)', () => {
  it('says what a small window means, never the number', () => {
    expect(modelNote(200_000, true)).toBe('');
    expect(modelNote(0, true)).toBe('');
    expect(modelNote(24_000, true)).toMatch(/^This model keeps less of long conversations in mind/);
    expect(modelNote(12_000, true)).toMatch(/^This model can only keep short conversations in mind/);
    expect(modelNote(4096, true)).toMatch(/^This model can't take actions/);
    expect(modelNote(200_000, false)).toMatch(/^This model can't take actions/);
    for (const n of [24_000, 12_000, 4096]) expect(modelNote(n, true)).not.toMatch(/\d|token|context/i);
  });
});

describe('provider entries', () => {
  it('lists the plan providers in order, with More providers last', () => {
    expect(providers.filter((p) => !p.more).map((p) => p.name)).toEqual(['Anthropic', 'OpenAI', 'Google Gemini', 'OpenRouter', 'Amazon Bedrock']);
    expect(providers.filter((p) => p.more).map((p) => p.name)).toEqual(['Groq', 'xAI', 'DeepSeek', 'Ollama', 'LM Studio', 'Other server']);
  });

  it('names hosts and regions for people', () => {
    expect(hostPort('http://127.0.0.1:9/v1')).toBe('127.0.0.1:9');
    expect(hostPort('https://models.example.com/v1')).toBe('models.example.com:443');
    expect(regionName('us-east-1')).toBe('US East (N. Virginia)');
    expect(regionName('xx-nowhere-1')).toBe('xx-nowhere-1');
  });
});
