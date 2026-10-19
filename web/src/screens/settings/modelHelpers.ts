// Plain words for models, providers and their state on Settings → Models
// (docs/design/ui.md §11.4, §14.3).
import type { ModelInfo, ProviderHealth } from '../../lib/api/types.gen';
import { plural } from '../../lib/format';
import { failure, type FailureCode } from '../../lib/providerErrors';
import { friendlyModel } from '../../lib/providers';

const providerNames: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Google Gemini',
  openrouter: 'OpenRouter',
  ollama: 'Ollama',
  lmstudio: 'LM Studio',
  bedrock: 'Amazon Bedrock',
  groq: 'Groq',
  xai: 'xAI',
  deepseek: 'DeepSeek',
  // "Other server" in the chooser; the server names its models' provider the same.
  custom: 'Model server',
};

/** "Anthropic" for the id or kind; an unknown id keeps its own spelling, capitalised. */
export function providerName(id: string, kind?: string): string {
  const known = providerNames[id] ?? (kind && kind !== 'openai_compat' ? providerNames[kind] : undefined);
  return known ?? (id ? id[0].toUpperCase() + id.slice(1) : '');
}

/** "<provider id>/<model>"; the model part may itself contain "/". */
export function splitRef(ref: string): { provider: string; model: string } {
  const i = ref.indexOf('/');
  return i < 0 ? { provider: '', model: ref } : { provider: ref.slice(0, i), model: ref.slice(i + 1) };
}

export function findModel(ref: string, models: ModelInfo[]): ModelInfo | undefined {
  const { provider, model } = splitRef(ref);
  return models.find((m) => m.ref.provider === provider && m.ref.model === model);
}

/** The friendly name ("Claude Sonnet 4.5"); without one (S5), one worked out from the id ("qwen3:4b" → "Qwen3 4B"). */
export function modelName(ref: string, models: ModelInfo[]): string {
  const m = findModel(ref, models);
  const name = m?.display_name || m?.display?.replace(/\s*\((local|free)\)$/i, '');
  return name || friendlyModel(splitRef(ref).model);
}

export function modelProvider(ref: string, models: ModelInfo[], kinds: Record<string, string> = {}): string {
  const m = findModel(ref, models);
  const id = splitRef(ref).provider;
  return m?.provider_name || providerName(id, kinds[id]);
}

// Bedrock regions with Claude or Nova models, by their console names.
export const regions: { code: string; name: string }[] = [
  { code: 'us-east-1', name: 'US East (N. Virginia)' },
  { code: 'us-east-2', name: 'US East (Ohio)' },
  { code: 'us-west-2', name: 'US West (Oregon)' },
  { code: 'ca-central-1', name: 'Canada (Central)' },
  { code: 'sa-east-1', name: 'South America (São Paulo)' },
  { code: 'eu-west-1', name: 'Europe (Ireland)' },
  { code: 'eu-west-2', name: 'Europe (London)' },
  { code: 'eu-west-3', name: 'Europe (Paris)' },
  { code: 'eu-central-1', name: 'Europe (Frankfurt)' },
  { code: 'eu-north-1', name: 'Europe (Stockholm)' },
  { code: 'ap-south-1', name: 'Asia Pacific (Mumbai)' },
  { code: 'ap-northeast-1', name: 'Asia Pacific (Tokyo)' },
  { code: 'ap-southeast-1', name: 'Asia Pacific (Singapore)' },
  { code: 'ap-southeast-2', name: 'Asia Pacific (Sydney)' },
];

export function regionName(code: string): string {
  return regions.find((r) => r.code === code)?.name ?? code;
}

// A chain is the slot's models in order: the first answers, the rest are backups.

/** Puts `ref` first in place of the current model; the backups stay as they were. */
export function makePrimary(chain: string[], ref: string): string[] {
  return [ref, ...chain.slice(1).filter((r) => r !== ref)];
}

/** Moves the backup at `index` (1 or more) by `delta`, never past the primary. */
export function moveBackup(chain: string[], index: number, delta: -1 | 1): string[] {
  const to = index + delta;
  if (index < 1 || to < 1 || to >= chain.length) return chain;
  const out = [...chain];
  [out[index], out[to]] = [out[to], out[index]];
  return out;
}

export function removeAt(chain: string[], index: number): string[] {
  return chain.filter((_, i) => i !== index);
}

export function addBackup(chain: string[], ref: string): string[] {
  return chain.includes(ref) ? chain : [...chain, ref];
}

export type Tone = 'muted' | 'warning' | 'danger';

export interface ProviderState {
  kind: string;
  enabled: boolean;
  health?: ProviderHealth;
  models: number;
  region?: string;
}

/** The status words on a provider row (§11.4); normal states stay muted. */
export function providerStatus(p: ProviderState): { text: string; tone: Tone } {
  if (!p.enabled) return { text: 'Off', tone: 'muted' };
  switch (p.health?.state) {
    case 'auth_failed':
      return { text: "Stopped accepting Bothy's key", tone: 'danger' };
    case 'down':
      return { text: "Can't reach it right now", tone: 'danger' };
    // Bothy couldn't reach it, or where its key is kept: never a refused key.
    case 'unreachable':
      return { text: "Can't reach it right now", tone: 'warning' };
    case 'degraded':
      return { text: 'Having trouble answering right now', tone: 'warning' };
  }
  // Like every provider: a Bedrock row's region and credentials are in its Manage dialog.
  return { text: p.models ? `Connected · ${plural(p.models, 'model')}` : 'Connected', tone: 'muted' };
}

const localKinds = new Set(['ollama', 'lmstudio']);

/** The main line of a failed check (§5.6.4), in the words setup uses. */
export function failureText(code: FailureCode, provider: string, kind = '', model?: string, secretRef = false): string {
  return failure(code, { name: provider, local: localKinds.has(kind), model, ollama: kind === 'ollama', secretRef }).text;
}

export type RefinePreset = 'standard' | 'thorough' | 'custom';

/** "How thorough" as agent.refine numbers; Standard is the server's default. */
export const refinePresets = {
  standard: { drafts: 3, depth: 2 },
  thorough: { drafts: 5, depth: 2 },
} as const;

export function refinePresetOf(drafts: number, depth: number): RefinePreset {
  for (const k of ['standard', 'thorough'] as const) if (refinePresets[k].drafts === drafts && refinePresets[k].depth === depth) return k;
  return 'custom';
}
