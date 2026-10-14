// App-wide shortcuts (docs/design/ui.md §6.4): ⌘K / Ctrl K and "/" search,
// "?" lists the shortcuts, ⌘⇧O / Ctrl Shift O starts a new chat. Single keys
// never fire while someone types in a field or a dialog is open.

export function isMac(): boolean {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
}

/** The modifier as the keyboard prints it. */
export const mod = () => (isMac() ? '⌘' : 'Ctrl');

const textInputs = new Set(['text', 'search', 'email', 'url', 'tel', 'password', 'number', 'date', 'datetime-local', 'time', 'month', 'week']);

export function typing(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  if (t.isContentEditable || t instanceof HTMLTextAreaElement || t instanceof HTMLSelectElement) return true;
  return t instanceof HTMLInputElement && textInputs.has(t.type);
}

export type Shortcut = 'search' | 'shortcuts' | 'new-chat';

/** The shortcut a key press asks for, or null. */
export function shortcutFor(e: KeyboardEvent): Shortcut | null {
  if (e.defaultPrevented || e.repeat || e.isComposing || document.querySelector('dialog[open]')) return null;
  const key = e.key.toLowerCase();
  const cmd = isMac() ? e.metaKey : e.ctrlKey;
  if (cmd && !e.altKey && !e.shiftKey && key === 'k') return 'search';
  if (cmd && e.shiftKey && !e.altKey && key === 'o') return 'new-chat';
  if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return null;
  if (e.key === '/') return 'search';
  if (e.key === '?') return 'shortcuts';
  return null;
}
