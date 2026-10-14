import { api, ApiError } from './api/client';
import type { ConfigPatchResp, ConfigView, Entry, FieldError, Source } from './api/types.gen';
import { ago } from './format';

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

export type SaveResult = { ok: true; restart: string[] } | { ok: false; errors: Record<string, string>; message: string; status?: number };

const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

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

  // {path...} takes the dotted path verbatim; keys such as model refs may hold "/".
  async #delete(path: string) {
    await api.del(`/api/config/${path.split('/').map(encodeURIComponent).join('/')}`);
    await this.load(true);
  }

  /**
   * Never toasts: the caller shows "Saved" next to the row and any error
   * under its control (docs/design/ui.md §11.1).
   */
  async save(changes: Record<string, unknown>): Promise<SaveResult> {
    for (const p of Object.keys(changes)) delete this.errors[p];
    try {
      const r = await api.patch<ConfigPatchResp>('/api/config', { set: changes });
      await this.load(true);
      return { ok: true, restart: r?.restart_required ?? [] };
    } catch (e) {
      return this.#failed(e);
    }
  }

  #failed(e: unknown): SaveResult {
    const errors: Record<string, string> = {};
    if (e instanceof ApiError) for (const f of e.fields) errors[f.path] = f.message;
    Object.assign(this.errors, errors);
    return { ok: false, errors, message: e instanceof Error ? e.message : String(e), status: e instanceof ApiError ? e.status : undefined };
  }

  /** The built-in value: the default layer under the effective one, else the schema default. */
  defaultValue(path: string): unknown {
    const e = this.entry(path);
    if (e?.source.layer === 'default') return e.value;
    const d = e?.shadowed?.find((s) => s.source.layer === 'default');
    return d ? d.value : this.schema(path)?.default;
  }

  /** True when the effective value differs from the built-in one. */
  changed(path: string): boolean {
    const e = this.entry(path);
    if (!e || e.source.layer === 'default') return false;
    return !same(e.value, this.defaultValue(path));
  }

  /**
   * Drops the UI override when what lies under it is already the default;
   * otherwise writes the default over a value from bothy.yaml or the
   * environment, which the UI layer cannot delete.
   */
  async resetToDefault(path: string): Promise<SaveResult> {
    const e = this.entry(path);
    const d = this.defaultValue(path);
    const below = e?.shadowed?.[0];
    if (e?.source.layer === 'ui' && (!below || same(below.value, d))) {
      try {
        await this.#delete(path);
        return { ok: true, restart: [] };
      } catch (err) {
        return this.#failed(err);
      }
    }
    return this.save({ [path]: d });
  }
}

/**
 * The sentence under a control after a failed save (§13.2). Main-flow pages
 * pass technical: false so config paths and server wording never show.
 */
export function saveErrorText(r: SaveResult, path?: string, opts: { technical?: boolean } = {}): string {
  if (r.ok) return '';
  if (r.status === 403) return "You can't do this. Ask an admin.";
  if (r.status === 409) return 'Someone else changed this setting. Reload to see their change.';
  const field = (path && r.errors[path]) || Object.values(r.errors)[0];
  if (opts.technical && field) {
    // "bothy.ui.yaml: budgets.daily_usd: must be 0 or more" → "Must be 0 or more."
    const reason = field.slice(field.lastIndexOf(': ') + 1).trim() || field;
    const sentence = reason.charAt(0).toUpperCase() + reason.slice(1);
    return `Couldn't save. ${/[.?]$/.test(sentence) ? sentence : sentence + '.'}`;
  }
  return "Couldn't save. Try again.";
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
