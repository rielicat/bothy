// Connected models by friendly name, which ones fill the main and quick-tasks
// slots, and the Refine defaults (§7.5). Slots come from the config, which only
// admins can read; members see "Main model" without its name.
import { api } from '../../lib/api/client';
import type { ConfigView, ModelInfo, RefineInfo } from '../../lib/api/types.gen';

export type RefineConfig = Pick<RefineInfo, 'enabled' | 'drafts' | 'depth' | 'multiplier'>;

const providerNames: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Google',
  ollama: 'Ollama',
  bedrock: 'Amazon Bedrock',
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

  /** "Claude Sonnet 4.5"; the id without its provider when the server has no name for it. */
  name(ref: string): string {
    const m = this.list.find((x) => `${x.ref.provider}/${x.ref.model}` === ref);
    return m?.display_name || m?.display || ref.slice(ref.indexOf('/') + 1);
  }
}

export const models = new Models();

/** "Sonnet 4.5" for narrow screens. */
export function shortName(name: string): string {
  return name.replace(/^Claude /, '').replace(/ \(local\)$/, '');
}

export const refOf = (m: ModelInfo) => `${m.ref.provider}/${m.ref.model}`;
