// Plain words for models, providers and their state on Settings → Models
// (docs/design/ui.md §11.4, §14.3).
import type { ModelInfo, ProbeResult, ProviderHealth } from '../../lib/api/types.gen';
import { plural } from '../../lib/format';

// Where a provider's credentials come from; GET /api/providers does not say yet (docs/design/ui.md §17).
export type ProviderHealthV2 = ProviderHealth & { source?: string };

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

/** The friendly name ("Claude Sonnet 4.5"); without one (S5), the model id without its provider prefix. */
export function modelName(ref: string, models: ModelInfo[]): string {
  const m = findModel(ref, models);
  const name = m?.display_name || m?.display?.replace(/\s*\((local|free)\)$/i, '');
  return name || splitRef(ref).model;
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
  health?: ProviderHealthV2;
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
    case 'degraded':
      return { text: 'Having trouble answering right now', tone: 'warning' };
  }
  if (p.kind === 'bedrock') return { text: `Connected · ${regionName(p.region || 'us-east-1')} · this server's AWS role`, tone: 'muted' };
  return { text: p.models ? `Connected · ${plural(p.models, 'model')}` : 'Connected', tone: 'muted' };
}

const localKinds = new Set(['ollama', 'lmstudio']);

/** The S13 category of a failed check; until the server sends one, a guess from the raw message. */
export function failureCode(r: { code?: string; detail?: string }, status?: number): string {
  if (r.code) return r.code;
  const d = (r.detail ?? '').toLowerCase();
  if (status === 401 || /\b401\b|invalid.*key|unauthori[sz]ed|api key/.test(d)) return 'key_rejected';
  if (/allow_private|private address/.test(d)) return 'private_address';
  if (/\b429\b|rate limit/.test(d)) return 'rate_limited';
  if (/credit|billing|quota/.test(d)) return 'out_of_credit';
  if (/deadline|timeout|timed out/.test(d)) return 'timeout';
  if (/x509|certificate|tls/.test(d)) return 'tls';
  if (/connection refused|no such host|dial tcp|unreachable/.test(d)) return 'unreachable';
  if (/\b5\d\d\b/.test(d)) return 'provider_error';
  return 'unknown';
}

/** The main line of a failed check (§5.6.4): what happened, then what to do. */
export function failureText(code: string, provider: string, kind = '', model = 'this model'): string {
  switch (code) {
    case 'key_rejected':
      return "That key wasn't accepted. Check you copied all of it, or create a new one.";
    case 'no_model_access':
      return `This key works, but it can't use ${model}. Choose another model.`;
    case 'out_of_credit':
      return `Your ${provider} account is out of credit. Add credit with ${provider}, then try again.`;
    case 'rate_limited':
      return `${provider} is limiting requests right now. Wait a minute and try again.`;
    case 'provider_error':
      return `${provider} isn't responding properly right now. Try again in a few minutes.`;
    case 'timeout':
      return `${provider} took too long to answer. Try again.`;
    case 'unreachable':
      return localKinds.has(kind)
        ? `${provider} isn't answering at that address. Make sure it's running.`
        : `Bothy couldn't reach ${provider} from this server. Check the server's internet connection or firewall.`;
    case 'private_address':
      return 'Bothy blocks addresses on your private network unless you allow them.';
    case 'no_models':
      return `${provider} is running but has no models yet. Install one on the server, then check again.`;
    case 'tls':
      return "The connection to that address isn't secure because of a certificate problem. Check the address. For a server on your own network you can use http://.";
    case 'not_a_model_server':
      return "That address answered, but it doesn't look like a model server. Check the address; it usually ends in /v1.";
    case 'aws_no_credentials':
      return "This server doesn't have AWS access right now. On AWS, give it a role with Bedrock access. On a laptop, sign in with the AWS CLI and restart Bothy.";
    case 'aws_denied':
      return "This server's AWS access isn't allowed to use Bedrock. Ask your AWS admin to add Bedrock permissions.";
    case 'aws_no_models':
      return 'No Claude or Nova models are turned on for this AWS account in this region. Turn on model access in the Bedrock console, or choose another region.';
    default:
      return 'Something went wrong while checking. Try again, or choose another option.';
  }
}

/** Refine's cost from configuration alone (S10 fallback): every draft plus one check. */
export function refineDrafts(preset: string): number {
  return preset === 'thorough' ? 8 : 3;
}

export function refineMultiplier(preset: string): number {
  return refineDrafts(preset) + 1;
}
