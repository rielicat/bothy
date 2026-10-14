import type { ProviderKind } from './api/types.gen';

export type Kind = ProviderKind;
export interface ProbeRequest {
  kind: Kind;
  base_url?: string;
  api_key?: string;
  model?: string;
  allow_private?: boolean;
  region?: string;
  /** A well-known key variable to check in place: the server never returns its value. */
  env_var?: string;
}

export interface ProviderDef {
  /** Written to providers.<id>. */
  id: string;
  name: string;
  kind: Kind;
  /** Line 2 in the provider list. */
  line: string;
  base?: string;
  /** Vault name for the API key. */
  key?: string;
  env?: string;
  console?: string;
  /** Recommended main and quick-tasks models; the probe's list decides when it has one. */
  model: string;
  quick?: string;
  /** Listed under "More providers". */
  more?: boolean;
  /** Runs on this computer or the network: a server address instead of a key. */
  local?: boolean;
  /** Any OpenAI-compatible server: an address and an optional key. */
  custom?: boolean;
}

export const providers: ProviderDef[] = [
  { id: 'anthropic', name: 'Anthropic', kind: 'anthropic', line: 'Claude models', key: 'anthropic_api_key', env: 'ANTHROPIC_API_KEY', console: 'https://console.anthropic.com', model: 'claude-sonnet-4-6', quick: 'claude-haiku-4-5' },
  { id: 'openai', name: 'OpenAI', kind: 'openai', line: 'GPT models', base: 'https://api.openai.com/v1', key: 'openai_api_key', env: 'OPENAI_API_KEY', console: 'https://platform.openai.com/api-keys', model: 'gpt-5.1', quick: 'gpt-5-mini' },
  { id: 'gemini', name: 'Google Gemini', kind: 'gemini', line: 'Gemini models', key: 'gemini_api_key', env: 'GEMINI_API_KEY', console: 'https://aistudio.google.com', model: 'gemini-2.5-pro', quick: 'gemini-2.5-flash' },
  { id: 'openrouter', name: 'OpenRouter', kind: 'openai_compat', line: 'Many models with one key', base: 'https://openrouter.ai/api/v1', key: 'openrouter_api_key', env: 'OPENROUTER_API_KEY', console: 'https://openrouter.ai/keys', model: 'anthropic/claude-sonnet-4.5', quick: 'anthropic/claude-haiku-4.5' },
  { id: 'bedrock', name: 'Amazon Bedrock', kind: 'bedrock', line: "Uses this server's AWS access", model: '' },
  { id: 'groq', name: 'Groq', kind: 'openai_compat', line: 'Fast open models', base: 'https://api.groq.com/openai/v1', key: 'groq_api_key', env: 'GROQ_API_KEY', console: 'https://console.groq.com/keys', model: 'llama-3.3-70b-versatile', more: true },
  { id: 'xai', name: 'xAI', kind: 'openai_compat', line: 'Grok models', base: 'https://api.x.ai/v1', key: 'xai_api_key', env: 'XAI_API_KEY', console: 'https://console.x.ai', model: 'grok-4', more: true },
  { id: 'deepseek', name: 'DeepSeek', kind: 'openai_compat', line: 'DeepSeek models', base: 'https://api.deepseek.com/v1', key: 'deepseek_api_key', env: 'DEEPSEEK_API_KEY', console: 'https://platform.deepseek.com', model: 'deepseek-chat', more: true },
  { id: 'ollama', name: 'Ollama', kind: 'ollama', line: 'On this computer or your network', base: 'http://host.docker.internal:11434', model: '', more: true, local: true },
  { id: 'lmstudio', name: 'LM Studio', kind: 'openai_compat', line: 'On this computer or your network', base: 'http://host.docker.internal:1234/v1', model: '', more: true, local: true },
  { id: 'custom', name: 'Other server', kind: 'openai_compat', line: 'Any OpenAI-compatible server', key: 'custom_api_key', model: '', more: true, custom: true },
];

export const provider = (id: string): ProviderDef => providers.find((p) => p.id === id) ?? providers[providers.length - 1];

/** "console.anthropic.com" for "Get a key at …". */
export const consoleLabel = (url: string) => url.replace(/^https?:\/\//, '');

export const regions: [code: string, name: string][] = [
  ['us-east-1', 'US East (N. Virginia)'],
  ['us-east-2', 'US East (Ohio)'],
  ['us-west-2', 'US West (Oregon)'],
  ['ca-central-1', 'Canada (Central)'],
  ['eu-central-1', 'Europe (Frankfurt)'],
  ['eu-west-1', 'Europe (Ireland)'],
  ['eu-west-2', 'Europe (London)'],
  ['eu-west-3', 'Europe (Paris)'],
  ['eu-north-1', 'Europe (Stockholm)'],
  ['ap-south-1', 'Asia Pacific (Mumbai)'],
  ['ap-northeast-1', 'Asia Pacific (Tokyo)'],
  ['ap-northeast-2', 'Asia Pacific (Seoul)'],
  ['ap-southeast-1', 'Asia Pacific (Singapore)'],
  ['ap-southeast-2', 'Asia Pacific (Sydney)'],
  ['sa-east-1', 'South America (São Paulo)'],
];

export const regionName = (code: string) => regions.find(([c]) => c === code)?.[1] ?? code;

const SPECIAL: Record<string, string> = { deepseek: 'DeepSeek', openai: 'OpenAI', gpt: 'GPT', oss: 'OSS', lm: 'LM', llava: 'LLaVA', phi: 'Phi', it: 'IT' };

function word(w: string): string {
  const lower = w.toLowerCase();
  if (SPECIAL[lower]) return SPECIAL[lower];
  // Sizes ("14b", "8x7b", "1.5b") and OpenAI's o-series ("o3") keep their own case.
  if (/^\d+(\.\d+)?(x\d+)?[bkm]$/i.test(w)) return w.slice(0, -1) + w.slice(-1).toUpperCase();
  if (/^o\d/.test(w) || /^\d/.test(w)) return w;
  return w[0].toUpperCase() + w.slice(1);
}

/**
 * A model's friendly name from its id, until the probe returns display names
 * (S5): "us.anthropic.claude-sonnet-4-5-20250929-v1:0" → "Claude Sonnet 4.5",
 * "qwen3:14b" → "Qwen3 14B".
 */
export function friendlyModel(id: string): string {
  let s = id.trim();
  if (!s) return s;
  s = s.replace(/^(?:(?:us|eu|apac|global|jp|au|ca|us-gov)\.)?(?:anthropic|amazon|meta|mistral|cohere|ai21|deepseek|openai|qwen)\./, '').replace(/-v\d+(?::\d+)?$/, '');
  s = s.slice(s.lastIndexOf('/') + 1);
  s = s.replace(/-\d{8}$/, '').replace(/-latest$/, '');
  const [base, tag] = s.split(':');
  let m = /^claude-(opus|sonnet|haiku|fable|mythos)-(\d+)(?:[-.](\d))?$/.exec(base);
  if (m) return `Claude ${word(m[1])} ${m[2]}${m[3] ? `.${m[3]}` : ''}`;
  m = /^claude-(\d)(?:[-.](\d))?-(opus|sonnet|haiku)$/.exec(base);
  if (m) return `Claude ${m[1]}${m[2] ? `.${m[2]}` : ''} ${word(m[3])}`;
  let name: string;
  m = /^gpt-([^-]+)(.*)$/.exec(base);
  if (m) name = ['GPT-' + m[1], ...m[2].split('-').filter(Boolean).map(word)].join(' ');
  else name = base.split(/[-_]/).filter(Boolean).map(word).join(' ');
  if (tag && tag !== 'latest') name += ' ' + tag.split('-').filter(Boolean).map(word).join(' ');
  return name;
}

const usable = (models: string[]) => models.filter((m) => !/embed/i.test(m));
const size = (m: string) => Number(/(\d+(?:\.\d+)?)b\b/i.exec(m)?.[1] ?? 0);

/** The largest local model by its parameter count, else the first. */
export function bestModel(models: string[]): string {
  const list = usable(models);
  return list.reduce((best, m) => (size(m) > size(best) ? m : best), list[0] ?? '');
}

function find(models: string[], want: string | RegExp | undefined): string | undefined {
  if (!want) return undefined;
  return models.find((m) => (typeof want === 'string' ? m === want || m.startsWith(want + '-') || m.startsWith(want + ':') : want.test(m)));
}

/** The main model to recommend and the quick-tasks model chosen silently (§5.6.3). */
export function pickModels(def: ProviderDef, models: string[]): { main: string; quick: string } {
  const list = usable(models);
  if (def.kind === 'bedrock') {
    const main = find(list, /claude-sonnet/) ?? find(list, /claude/) ?? find(list, /nova-pro/) ?? list[0] ?? '';
    return { main, quick: find(list, /claude-haiku/) ?? find(list, /nova-(lite|micro)/) ?? main };
  }
  if (def.local || def.custom || !def.model) {
    const main = bestModel(list);
    return { main, quick: main };
  }
  const main = !list.length ? def.model : (find(list, def.model) ?? list[0]);
  const quick = def.quick && (!list.length || find(list, def.quick)) ? (find(list, def.quick) ?? def.quick) : main;
  return { main, quick };
}

/** Models for the "Change" select: friendly names, the recommended one first. */
export function modelOptions(models: string[], recommended: string): { value: string; label: string }[] {
  const seen = new Set<string>();
  const out: { value: string; label: string }[] = [];
  for (const m of [recommended, ...usable(models)]) {
    const label = friendlyModel(m);
    if (!m || seen.has(label)) continue;
    seen.add(label);
    out.push({ value: m, label: m === recommended ? `${label} (recommended)` : label });
  }
  return out;
}

/** What a small window or missing tool use means for the person (§5.6.3); empty when nothing does. */
export function modelNote(context: number, tools: boolean): string {
  if ((context > 0 && context < 8192) || !tools) {
    return "This model can't take actions like reading files or searching the web, so Bothy can only chat with it. Choose a larger model to let Bothy do work.";
  }
  if (context > 0 && context < 16384) return 'This model can only keep short conversations in mind, so Bothy may forget earlier messages. A larger model works better.';
  if (context > 0 && context < 32768) return 'This model keeps less of long conversations in mind. Bothy summarises older messages to keep going.';
  return '';
}

/** host:port of an address, for "Allow {host:port}". */
export function hostPort(address: string): string {
  try {
    const u = new URL(address);
    return `${u.hostname}:${u.port || (u.protocol === 'https:' ? '443' : '80')}`;
  } catch {
    return address;
  }
}

export const portOf = (address: string) => hostPort(address).split(':').pop() ?? '';

/** The providers.<id> entry written on Continue. */
export function providerEntry(def: ProviderDef, o: { base?: string; key?: boolean; allowPrivate?: boolean; region?: string }): Record<string, unknown> {
  const entry: Record<string, unknown> = {
    id: def.id,
    type: def.kind,
    base_url: o.base ?? def.base ?? '',
    api_key: o.key && def.key ? `secret:${def.key}` : '',
    allow_private: !!o.allowPrivate,
    discover: def.kind === 'ollama',
  };
  if (o.region) entry.region = o.region;
  return entry;
}

/** @deprecated v0.1 window labels; only ChainEditor (C7) and format.test.ts still use them, and both go before C10. */
export function windowNote(context: number): string {
  if (!context) return '';
  if (context < 8192) return 'text-only';
  if (context < 16384) return 'degraded';
  if (context < 32768) return 'compact mode';
  return '';
}
