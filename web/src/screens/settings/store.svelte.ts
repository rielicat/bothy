// The effective config as Settings → General, Models and People read and
// write it (SPEC §7.3), and the save state of one settings row (§11.1).
import { api, ApiError } from '../../lib/api/client';
import type { ConfigPatchResp, ConfigView, Entry } from '../../lib/api/types.gen';
import { events } from '../../lib/sse.svelte';

class SettingsConfig {
  view: ConfigView | null = $state(null);
  failed = $state(false);
  #byPath = $derived(new Map((this.view?.entries ?? []).map((e) => [e.path, e])));
  #loading: Promise<void> | null = null;

  load(force = false): Promise<void> {
    if (this.view && !force) return Promise.resolve();
    this.#loading ??= api
      .get<ConfigView>('/api/config')
      .then((v) => {
        this.view = v;
        this.failed = false;
      })
      .catch(() => {
        this.failed = !this.view;
      })
      .finally(() => (this.#loading = null));
    return this.#loading;
  }

  entry(path: string): Entry | undefined {
    return this.#byPath.get(path);
  }

  /** The server knows this setting; rows for settings it doesn't know yet stay hidden. */
  has(path: string): boolean {
    return this.#byPath.has(path);
  }

  value<T = unknown>(path: string, fallback?: T): T {
    const e = this.#byPath.get(path);
    return (e && e.value !== null && e.value !== undefined ? e.value : fallback) as T;
  }

  /** Changes to this setting apply only after a restart. */
  restartOnly(path: string): boolean {
    return this.#byPath.get(path)?.hot === false;
  }

  locked(path: string): boolean {
    return !!this.#byPath.get(path)?.locked;
  }

  /** Keys of the list items or map entries under prefix: "providers" → ["anthropic", "ollama"]. */
  keys(prefix: string): string[] {
    const p = prefix + '.';
    return [...new Set((this.view?.entries ?? []).filter((e) => e.path.startsWith(p)).map((e) => e.path.slice(p.length).split('.')[0]))];
  }

  /** Writes and reloads; throws ApiError (with field errors on 400). Returns the paths that need a restart, which the shell's banner announces. */
  async save(set: Record<string, unknown>): Promise<string[]> {
    const r = await api.patch<ConfigPatchResp>('/api/config', { set });
    await this.load(true);
    return r?.restart_required ?? [];
  }

  /**
   * Reloads when the config changes elsewhere (another admin, another tab);
   * a row someone is editing then notices its value moved and shows a conflict.
   */
  listen(): () => void {
    return events.on('config.changed', () => void this.load(true));
  }
}

export const settingsConfig = new SettingsConfig();

/** budgets.per_user_daily_usd for a username or "<app>:<id>"; null when there is none. */
export function personalLimit(key: string): number | null {
  const own = settingsConfig.value<number | null>(`budgets.per_user_daily_usd.${key}`, null);
  if (typeof own === 'number') return own;
  const map = settingsConfig.value<Record<string, number>>('budgets.per_user_daily_usd', {});
  return typeof map?.[key] === 'number' ? map[key] : null;
}

/** The sentence under a control when a save fails (§13.2). */
export function saveError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) return "You can't do this. Ask an admin.";
    if (e.status === 409) return 'Someone else changed this setting. Reload to see their change.';
    if (e.status === 503 || e.status === 502) return "Couldn't save. Bothy isn't responding, so try again in a moment.";
  } else if (e instanceof TypeError) return "Couldn't save. Bothy didn't respond.";
  return "Couldn't save. Try again.";
}

/** One row's saving state: "Saving…", then "Saved" for 2 s (SettingsRow shows it), or the error under the control. */
export class Saver {
  status: 'saving' | 'saved' | null = $state(null);
  error = $state('');
  /** What the last failed run threw, for rows with their own wording. */
  lastError: unknown = null;
  #timer: ReturnType<typeof setTimeout> | undefined;

  async run(fn: () => Promise<unknown>): Promise<boolean> {
    clearTimeout(this.#timer);
    this.status = 'saving';
    this.error = '';
    this.lastError = null;
    try {
      await fn();
      this.done();
      return true;
    } catch (e) {
      this.status = null;
      this.lastError = e;
      this.error = saveError(e);
      return false;
    }
  }

  /** Marks a save made elsewhere (a dialog that shows its own errors) as done. */
  done() {
    clearTimeout(this.#timer);
    this.error = '';
    this.status = 'saved';
    this.#timer = setTimeout(() => {
      if (this.status === 'saved') this.status = null;
    }, 2000);
  }

  fail(message: string) {
    this.status = null;
    this.error = message;
  }

  clear() {
    this.error = '';
  }
}

/** The help line, plus the restart note on settings that only apply after one (§11.1). */
export function helpFor(path: string, help?: string): string | undefined {
  if (!settingsConfig.restartOnly(path)) return help;
  return help ? `${help} Takes effect after a restart.` : 'Takes effect after a restart.';
}
