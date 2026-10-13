import { resolve, type Match } from './routes';

// Only same-origin absolute paths; "//host" and "/\host" are protocol-relative
// in browsers and would turn ?next= (or a server-sent link) into an open
// redirect. The parse catches the rest, such as "/\t/host": browsers drop tabs
// and newlines inside URLs.
export function safeNext(next: string | null | undefined, fallback = '/chat'): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return fallback;
  try {
    if (new URL(next, window.location.origin).origin !== window.location.origin) return fallback;
  } catch {
    return fallback;
  }
  return next;
}

class Router {
  path = $state(window.location.pathname);
  search = $state(window.location.search);
  hash = $state(window.location.hash);
  current: Match | null = $derived(resolve(this.path));
  query: URLSearchParams = $derived(new URLSearchParams(this.search));

  constructor() {
    const sync = () => {
      this.path = window.location.pathname;
      this.search = window.location.search;
      this.hash = window.location.hash;
    };
    window.addEventListener('popstate', sync);
    window.addEventListener('hashchange', sync);
  }

  navigate(to: string, opts: { replace?: boolean } = {}) {
    const url = new URL(to, window.location.origin);
    if (url.origin !== window.location.origin) return;
    if (opts.replace) history.replaceState(null, '', url);
    else history.pushState(null, '', url);
    this.path = url.pathname;
    this.search = url.search;
    this.hash = url.hash;
  }

  // Merges params into the query string (empty values remove the key).
  setQuery(params: Record<string, string | null | undefined>, opts: { replace?: boolean } = { replace: true }) {
    const q = new URLSearchParams(this.search);
    for (const [k, v] of Object.entries(params)) {
      if (v) q.set(k, v);
      else q.delete(k);
    }
    const s = q.toString();
    this.navigate(this.path + (s ? `?${s}` : ''), opts);
  }

  // onclick handler for in-app <a href> links; modified clicks keep browser behaviour.
  link = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.currentTarget as HTMLAnchorElement;
    if (a.target || a.origin !== window.location.origin) return;
    e.preventDefault();
    this.navigate(a.pathname + a.search + a.hash);
  };
}

export const router = new Router();
