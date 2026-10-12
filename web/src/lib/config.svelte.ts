import { api, ApiError } from './api/client';
import type { ConfigPatchResp, ConfigView, Entry, FieldError, Source } from './api/types.gen';
import { ago } from './format';
import { toasts } from './stores/ui.svelte';

export interface SchemaNode {
  type?: string | string[];
  enum?: unknown[];
  format?: string;
  default?: unknown;
  minimum?: number;
  maximum?: number;
  properties?: Record<string, SchemaNode>;
  items?: SchemaNode;
  additionalProperties?: SchemaNode | boolean;
  'x-locked'?: boolean;
}

// Effective config (SPEC §7.3) for the settings pages: one fetch, entries by
// dotted path, and PATCH/DELETE helpers that refresh it.
class Config {
  view: ConfigView | null = $state(null);
  error = $state('');
  errors: Record<string, string> = $state({});
  #byPath = $derived(new Map((this.view?.entries ?? []).map((e) => [e.path, e])));
  #loading: Promise<void> | null = null;

  load(force = false): Promise<void> {
    if (this.view && !force) return Promise.resolve();
    this.#loading ??= api
      .get<ConfigView>('/api/config?schema=1')
      .then((v) => {
        this.view = v;
        this.error = '';
      })
      .catch((e) => {
        this.error = e instanceof Error ? e.message : String(e);
      })
      .finally(() => (this.#loading = null));
    return this.#loading;
  }

  entry(path: string): Entry | undefined {
    return this.#byPath.get(path);
  }

  value<T = unknown>(path: string, fallback?: T): T {
    const e = this.#byPath.get(path);
    return (e ? e.value : fallback) as T;
  }

  flag(path: string): boolean {
    return this.#byPath.get(path)?.value === true;
  }

  // Keys under prefix: "item" = list item ids / struct keys (first segment),
  // "leaf" = map keys whose values are scalars (the whole rest), "tail" = map
  // keys of objects whose keys may contain dots, such as model refs (all but
  // the last segment).
  keys(prefix: string, mode: 'item' | 'leaf' | 'tail' = 'item'): string[] {
    const out = new Set<string>();
    const p = prefix + '.';
    for (const e of this.view?.entries ?? []) {
      if (!e.path.startsWith(p)) continue;
      const rest = e.path.slice(p.length);
      out.add(mode === 'leaf' ? rest : mode === 'tail' ? rest.slice(0, rest.lastIndexOf('.')) : rest.split('.')[0]);
    }
    return [...out];
  }

  schema(path: string): SchemaNode | undefined {
    let node = this.view?.schema as SchemaNode | undefined;
    const segs = path.split('.');
    for (let i = 0; i < segs.length && node; i++) {
      if (node.properties?.[segs[i]]) node = node.properties[segs[i]];
      else if (node.items?.properties) {
        // list item addressed by id: skip the id segment(s) up to a known field
        const rest = segs.slice(i);
        const k = rest.findIndex((s) => node!.items!.properties![s]);
        if (k < 0) return node.items;
        node = node.items.properties[rest[k]];
        i += k;
      } else if (typeof node.additionalProperties === 'object') node = node.additionalProperties;
      else return undefined;
    }
    return node;
  }

  // Returns the field errors (also stored per path) or null on success.
  async set(changes: Record<string, unknown>, quiet = false): Promise<FieldError[] | null> {
    for (const p of Object.keys(changes)) delete this.errors[p];
    try {
      const r = await api.patch<ConfigPatchResp>('/api/config', { set: changes });
      await this.load(true);
      if (r?.restart_required?.length) toasts.push(`Saved. Restart Bothy to apply: ${r.restart_required.join(', ')}`, 'info', 8000);
      else if (!quiet) toasts.ok('Saved');
      return null;
    } catch (e) {
      if (e instanceof ApiError && e.fields.length) {
        for (const f of e.fields) this.errors[f.path] = f.message;
        return e.fields;
      }
      toasts.error(e);
      return [{ path: '', source: { layer: 'ui' }, message: String(e) }];
    }
  }

  async reset(path: string) {
    try {
      // {path...} takes the dotted path verbatim; keys such as model refs may hold "/".
      await api.del(`/api/config/${path.split('/').map(encodeURIComponent).join('/')}`);
      await this.load(true);
      toasts.ok('Reset to the value from the next layer');
    } catch (e) {
      toasts.error(e);
    }
  }
}

export const config = new Config();

export function sourceLabel(s: Source, me?: string): string {
  switch (s.layer) {
    case 'file':
      return `file · ${(s.file ?? 'bothy.yaml').split('/').pop()}${s.line ? `:${s.line}` : ''}`;
    case 'env':
      return `env · ${s.env_var ?? ''}`;
    case 'ui':
      return `UI · ${s.by === `user:${me}` ? 'you' : (s.by ?? '').replace('user:', '')}${s.at ? ` · ${ago(s.at)}` : ''}`;
    default:
      return 'default';
  }
}

export function show(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—';
  return typeof v === 'string' ? v : JSON.stringify(v);
}
