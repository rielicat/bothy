import type { ProviderKind } from './api/types.gen';

export interface Tile {
  id: string; // provider id written to providers.<id>
  name: string;
  kind: ProviderKind;
  base?: string;
  key?: string; // vault name for the API key
  env?: string; // well-known env var the wizard may offer to import
  model: string; // suggested main model
  fast?: string; // suggested fast model
  local?: boolean; // private address: allow_private on its exact host:port
  custom?: boolean;
}

// Suggested models are only defaults for the probe; the picker lists what
// the provider and the catalog actually report.
export const tiles: Tile[] = [
  { id: 'anthropic', name: 'Anthropic', kind: 'anthropic', key: 'anthropic_api_key', env: 'ANTHROPIC_API_KEY', model: 'claude-sonnet-4-6', fast: 'claude-haiku-4-5' },
  { id: 'openai', name: 'OpenAI', kind: 'openai', base: 'https://api.openai.com/v1', key: 'openai_api_key', env: 'OPENAI_API_KEY', model: 'gpt-5.1', fast: 'gpt-5-mini' },
  { id: 'gemini', name: 'Gemini', kind: 'gemini', key: 'gemini_api_key', env: 'GEMINI_API_KEY', model: 'gemini-2.5-pro', fast: 'gemini-2.5-flash' },
  { id: 'openrouter', name: 'OpenRouter', kind: 'openai_compat', base: 'https://openrouter.ai/api/v1', key: 'openrouter_api_key', env: 'OPENROUTER_API_KEY', model: 'anthropic/claude-sonnet-4.5' },
  { id: 'groq', name: 'Groq', kind: 'openai_compat', base: 'https://api.groq.com/openai/v1', key: 'groq_api_key', env: 'GROQ_API_KEY', model: 'llama-3.3-70b-versatile' },
  { id: 'xai', name: 'xAI', kind: 'openai_compat', base: 'https://api.x.ai/v1', key: 'xai_api_key', env: 'XAI_API_KEY', model: 'grok-4' },
  { id: 'deepseek', name: 'DeepSeek', kind: 'openai_compat', base: 'https://api.deepseek.com/v1', key: 'deepseek_api_key', env: 'DEEPSEEK_API_KEY', model: 'deepseek-chat' },
  { id: 'ollama', name: 'Ollama', kind: 'ollama', base: 'http://host.docker.internal:11434', local: true, model: 'qwen3:14b' },
  { id: 'lmstudio', name: 'LM Studio', kind: 'openai_compat', base: 'http://host.docker.internal:1234/v1', local: true, model: '' },
  { id: 'custom', name: 'Custom', kind: 'openai_compat', base: '', key: 'custom_api_key', model: '', custom: true },
];

export function windowNote(context: number): string {
  if (!context) return '';
  if (context < 8192) return 'text-only';
  if (context < 16384) return 'degraded';
  if (context < 32768) return 'compact mode';
  return '';
}
