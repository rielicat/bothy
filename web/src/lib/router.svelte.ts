import { resolve, type Match } from './routes';

class Router {
  path = $state(window.location.pathname);
  current: Match | null = $derived(resolve(this.path));

  constructor() {
    window.addEventListener('popstate', () => {
      this.path = window.location.pathname;
    });
  }

  navigate(to: string, opts: { replace?: boolean } = {}) {
    const url = new URL(to, window.location.origin);
    if (url.origin !== window.location.origin) return;
    if (opts.replace) history.replaceState(null, '', url);
    else history.pushState(null, '', url);
    this.path = url.pathname;
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
