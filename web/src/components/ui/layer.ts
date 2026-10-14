// Floating layers (menus, listboxes, tooltips): shown in the top layer with the
// Popover API so no scroll container or dialog clips them, and placed next to
// their anchor with CSSOM writes (the CSP has no 'unsafe-inline').
import type { Attachment } from 'svelte/attachments';

export type Placement = 'bottom-start' | 'bottom-end' | 'top-start' | 'top-end' | 'top' | 'bottom';

const MARGIN = 8;

/**
 * 'manual' where the Popover API exists, otherwise no attribute: some
 * environments hide [popover] without being able to show it (jsdom), and
 * there a fixed, z-indexed layer is the fallback.
 */
export const popoverAttr: 'manual' | undefined = typeof HTMLElement !== 'undefined' && 'showPopover' in HTMLElement.prototype ? 'manual' : undefined;

export function showLayer(el: HTMLElement) {
  if (typeof el.showPopover !== 'function' || !el.hasAttribute('popover')) return;
  try {
    el.showPopover();
  } catch {
    // already showing, or detached between scheduling and showing
  }
}

/** Places `el` beside `anchor`, flipping to the other side and clamping to the viewport so it stays on screen. */
export function place(anchor: Element, el: HTMLElement, placement: Placement, opts: { offset?: number; matchWidth?: boolean } = {}) {
  const offset = opts.offset ?? 4;
  const a = anchor.getBoundingClientRect();
  if (opts.matchWidth) el.style.minWidth = `${Math.round(a.width)}px`;
  const r = el.getBoundingClientRect();
  const vw = document.documentElement.clientWidth || window.innerWidth;
  const vh = window.innerHeight;
  const [side, align] = placement.split('-') as ['top' | 'bottom', 'start' | 'end' | undefined];
  const below = a.bottom + offset;
  const above = a.top - offset - r.height;
  let top = side === 'top' ? above : below;
  if (side === 'bottom' && below + r.height > vh - MARGIN && above >= MARGIN) top = above;
  if (side === 'top' && above < MARGIN && below + r.height <= vh - MARGIN) top = below;
  let left = align === 'start' ? a.left : align === 'end' ? a.right - r.width : a.left + a.width / 2 - r.width / 2;
  left = Math.max(MARGIN, Math.min(left, vw - r.width - MARGIN));
  top = Math.max(MARGIN, Math.min(top, vh - r.height - MARGIN));
  el.style.top = `${Math.round(top)}px`;
  el.style.left = `${Math.round(left)}px`;
}

/** Shows the element as a top-layer popover beside its anchor and keeps it there while the page scrolls or resizes. */
export function floating(anchor: () => Element | null | undefined, placement: Placement, opts: { offset?: number; matchWidth?: boolean } = {}): Attachment<HTMLElement> {
  return (el) => {
    showLayer(el);
    const update = () => {
      const a = anchor();
      if (a) place(a, el, placement, opts);
    };
    update();
    window.addEventListener('scroll', update, true);
    window.addEventListener('resize', update);
    return () => {
      window.removeEventListener('scroll', update, true);
      window.removeEventListener('resize', update);
    };
  };
}

/** Calls `fn` on a pointer press outside every element `inside` returns. */
export function outside(inside: () => (Element | null | undefined)[], fn: () => void): Attachment<HTMLElement> {
  return () => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node | null;
      if (t && inside().some((el) => el?.contains(t))) return;
      fn();
    };
    document.addEventListener('pointerdown', down, true);
    return () => document.removeEventListener('pointerdown', down, true);
  };
}

/** Typeahead buffer: keys typed within half a second build one search string. */
export function typeahead() {
  let buffer = '';
  let at = 0;
  return (key: string): string => {
    const now = Date.now();
    buffer = now - at > 500 ? key : buffer + key;
    at = now;
    const q = buffer.toLowerCase();
    // "ppp" cycles through the items starting with "p" rather than looking for "ppp".
    return [...q].every((c) => c === q[0]) ? q[0] : q;
  };
}

/** Index of the first label starting with `query`, searching after `from` and wrapping; -1 when none. */
export function findTypeahead(labels: string[], query: string, from: number): number {
  const n = labels.length;
  // A repeated single letter cycles through the matches instead of staying put.
  const start = query.length === 1 ? from + 1 : from;
  for (let k = 0; k < n; k++) {
    const i = (((start + k) % n) + n) % n;
    if (labels[i].toLowerCase().startsWith(query)) return i;
  }
  return -1;
}

export function prefersReducedMotion(): boolean {
  return !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
}

/** Hover-capable fine pointer: the only input where tooltips and shortcut hints appear (§3.3). */
export function finePointer(): boolean {
  return !!window.matchMedia?.('(hover: hover) and (pointer: fine)').matches;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';

export function focusables(root: Element): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
    (el) => !el.closest('[inert], [hidden]') && (typeof el.checkVisibility !== 'function' || el.checkVisibility({ visibilityProperty: true })),
  );
}

/** Keeps Tab and Shift+Tab inside `root` (dialogs and drawers). */
export function trapTab(e: KeyboardEvent, root: Element) {
  if (e.key !== 'Tab') return;
  const items = focusables(root);
  if (!items.length) {
    e.preventDefault();
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement;
  if (e.shiftKey && (active === first || !root.contains(active))) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && (active === last || !root.contains(active))) {
    e.preventDefault();
    first.focus();
  }
}
