// Connected models by friendly name, which ones fill the main and quick-tasks
// slots, and the Refine defaults (§7.5). Slots come from the config, which only
// admins can read; members see "Main model" without its name.
import { api } from '../../lib/api/client';
import type { ConfigView, ModelInfo, RefineInfo } from '../../lib/api/types.gen';
import { friendlyModel } from '../../lib/providers';

export type RefineConfig = Pick<RefineInfo, 'enabled' | 'drafts' | 'depth' | 'multiplier'>;

const providerNames: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Google',
  google: 'Google',
  ollama: 'Ollama',
  bedrock: 'Amazon Bedrock',
  openrouter: 'OpenRouter',
  groq: 'Groq',
  deepseek: 'DeepSeek',
  xai: 'xAI',
  mistral: 'Mistral',
  lmstudio: 'LM Studio',
};

export const providerName = (id: string) => providerNames[id] ?? id.charAt(0).toUpperCase() + id.slice(1);

class Models {
  list: ModelInfo[] = $state([]);
  main = $state('');
  fast = $state('');
  refine: RefineConfig | null = $state(null);
  #loading: Promise<void> | null = null;

  load(admin: boolean): Promise<void> {
    this.#loading ??= (async () => {
      const [list, cfg, refine] = await Promise.all([
        api.get<ModelInfo[]>('/api/models').catch(() => []),
        admin ? api.get<ConfigView>('/api/config').catch(() => null) : null,
        api.get<RefineInfo>('/api/refine').catch(() => null),
      ]);
      this.list = list ?? [];
      const get = (p: string) => cfg?.entries.find((e) => e.path === p)?.value;
      const first = (v: unknown) => (Array.isArray(v) && typeof v[0] === 'string' ? v[0] : '');
      this.main = first(get('models.main'));
      this.fast = first(get('models.fast'));
      this.refine = refine ? { enabled: refine.enabled, drafts: refine.drafts, depth: refine.depth, multiplier: refine.multiplier } : null;
    })();
    return this.#loading;
  }

  /** "Claude Sonnet 4.5". Ids stay in Chat details → Technical details (§14.3). */
  name(ref: string): string {
    const m = this.list.find((x) => `${x.ref.provider}/${x.ref.model}` === ref);
    const named = m?.display_name || m?.display;
    if (named) return named;
    // A chat can keep a model that has since been disconnected. With no list
    // at all there is nothing to compare against, and the slots stay configured.
    if (this.list.length && ref !== this.main && ref !== this.fast) return "A model that's no longer connected";
    return friendlyModel(ref.slice(ref.indexOf('/') + 1));
  }
}

export const models = new Models();

/** "Sonnet 4.5" for narrow screens. */
export function shortName(name: string): string {
  return name.replace(/^Claude /, '').replace(/ \(local\)$/, '');
}

export const refOf = (m: ModelInfo) => `${m.ref.provider}/${m.ref.model}`;
