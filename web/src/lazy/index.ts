import type { Component } from 'svelte';
import { resolve, type Match } from '../lib/routes';

// Screens take different props (all accept params), so the tables are loosely typed.
export type Screen = Component<any>;

export interface RouteGroup {
  /** The screen for a match inside the group, or null when the address names nothing. */
  screen(m: Match): Screen | null;
}

// Each group is its own chunk, so the first screen loads only chat, the shell
// and the primitives they share (docs/design/ui.md §18.2 G1).
const loaders = {
  auth: () => import('./auth'),
  approvals: () => import('./approvals'),
  knowledge: () => import('./knowledge'),
  activity: () => import('./activity'),
  settings: () => import('./settings'),
} satisfies Record<string, () => Promise<RouteGroup>>;

export type Group = keyof typeof loaders;

/** The chunk a route pattern lives in, or null for the screens in the entry (chat, the gallery). */
export function groupOf(pattern: string): Group | null {
  if (pattern === '/setup' || pattern === '/login' || pattern === '/password') return 'auth';
  const top = pattern.split('/')[1];
  return top === 'approvals' || top === 'knowledge' || top === 'activity' || top === 'settings' ? top : null;
}

const started = new Map<Group, Promise<RouteGroup>>();

export function load(g: Group): Promise<RouteGroup> {
  let p = started.get(g);
  if (!p) {
    p = loaders[g]();
    started.set(g, p);
    // Forgotten on failure, so the next hover or visit fetches it again.
    p.catch(() => started.delete(g));
  }
  return p;
}

/** Starts loading the chunk behind an in-app path; a failure waits for the visit that needs it. */
export function prefetch(path: string) {
  const m = resolve(path);
  const g = m && groupOf(m.pattern);
  if (g) load(g).catch(() => {});
}

/** Prefetches as soon as a pointer, finger or focus reaches an in-app link, ahead of the click. */
export function prefetchLinks(): () => void {
  let last: Element | null = null;
  const on = (e: Event) => {
    const a = e.target instanceof Element ? e.target.closest('a[href]') : null;
    if (a === last) return;
    last = a;
    if (a instanceof HTMLAnchorElement && a.origin === window.location.origin) prefetch(a.pathname);
  };
  const types = ['pointerover', 'focusin', 'touchstart'] as const;
  for (const t of types) document.addEventListener(t, on, { capture: true, passive: true });
  return () => types.forEach((t) => document.removeEventListener(t, on, { capture: true }));
}

/** Loads the groups one at a time while the browser is idle, so later visits are instant. */
export function prefetchIdle(groups: Group[]): () => void {
  if ((navigator as Navigator & { connection?: { saveData?: boolean } }).connection?.saveData) return () => {};
  let stopped = false;
  let cancel = () => {};
  const idle = (f: () => void) => {
    // Safari has no requestIdleCallback.
    if (typeof window.requestIdleCallback === 'function') {
      const id = window.requestIdleCallback(f, { timeout: 4000 });
      cancel = () => window.cancelIdleCallback(id);
    } else {
      const id = setTimeout(f, 1000);
      cancel = () => clearTimeout(id);
    }
  };
  const next = (i: number) => {
    if (stopped || i >= groups.length) return;
    idle(() => {
      if (!stopped) load(groups[i]).then(() => next(i + 1), () => {});
    });
  };
  next(0);
  return () => {
    stopped = true;
    cancel();
  };
}
