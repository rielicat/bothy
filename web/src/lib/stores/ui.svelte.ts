import { errorText } from '../api/client';

export type ThemePref = 'system' | 'light' | 'dark';

const KEY = 'bothy.theme';

function readPref(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'light' || v === 'dark') return v;
  } catch {
    // storage blocked (private mode): fall back to the system preference
  }
  return 'system';
}

class Theme {
  pref: ThemePref = $state(readPref());

  set(p: ThemePref) {
    this.pref = p;
    try {
      if (p === 'system') localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, p);
    } catch {
      // not persisted; still applied for this page
    }
    this.apply();
  }

  apply() {
    const dark = this.pref === 'dark' || (this.pref === 'system' && window.matchMedia?.('(prefers-color-scheme: dark)').matches);
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
  }
}

export const theme = new Theme();

export interface Toast {
  id: number;
  text: string;
  kind: 'ok' | 'error' | 'info';
}

class Toasts {
  items: Toast[] = $state([]);
  #id = 0;

  push(text: string, kind: Toast['kind'] = 'info', ttl = kind === 'error' ? 8000 : 4000) {
    const id = ++this.#id;
    this.items.push({ id, text, kind });
    setTimeout(() => this.dismiss(id), ttl);
  }

  ok(text: string) {
    this.push(text, 'ok');
  }

  error(e: unknown) {
    this.push(errorText(e), 'error');
  }

  dismiss(id: number) {
    this.items = this.items.filter((t) => t.id !== id);
  }
}

export const toasts = new Toasts();

// Runs fn, reports a failure as a toast and says whether it succeeded.
export async function attempt(fn: () => Promise<unknown>, okText?: string): Promise<boolean> {
  try {
    await fn();
    if (okText) toasts.ok(okText);
    return true;
  } catch (e) {
    toasts.error(e);
    return false;
  }
}

export async function copyText(text: string, what = 'Copied') {
  try {
    await navigator.clipboard.writeText(text);
    toasts.ok(what);
  } catch {
    toasts.push('Copy failed: select the text and copy it manually.', 'error');
  }
}
