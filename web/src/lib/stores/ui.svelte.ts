import type { Snippet } from 'svelte';

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

// The shell's overlays, opened from the sidebar, the account menu, shortcuts
// and other screens (Security opens the pause dialog too).
class Shell {
  drawer = $state(false);
  search = $state(false);
  shortcuts = $state(false);
  pause = $state(false);
  unlock = $state(false);
  /** The page's control for the right end of the phone top bar (§4.4). */
  actions: Snippet | null = $state(null);
  /** Where "Back to Bothy" returns: the last address outside Settings. */
  back = $state('/chat');
}

export const shell = new Shell();

/**
 * Shows a page's control (the chat ⋯, a project's Edit) at the right of the
 * phone top bar while the page is mounted: `$effect(() => topBarActions(menu))`.
 */
export function topBarActions(s: Snippet): () => void {
  shell.actions = s;
  return () => {
    if (shell.actions === s) shell.actions = null;
  };
}

