// Shared by the Security and Advanced pages.
import { tick } from 'svelte';
import { relTime } from '../../../lib/format';

/** After a refused submit, focus goes to the first field with an error (§5.2, §16). */
export async function focusInvalid(form: EventTarget | null) {
  await tick();
  (form as HTMLElement | null)?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
}

// Below 1024 px the settings sidebar is a drawer, so each group page links
// back to the phone /settings list (§4.3); on desktops the sidebar is there.
class Wide {
  current = $state(false);

  constructor() {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia('(min-width: 1024px)');
    this.current = mq.matches;
    mq.addEventListener?.('change', (e) => (this.current = e.matches));
  }
}

const wide = new Wide();

export function settingsBack(): { href: string; label: string } | undefined {
  return wide.current ? undefined : { href: '/settings', label: 'Settings' };
}

export const securityBack = { href: '/settings/security', label: 'Security' };

/** relTime for the middle of a sentence: "last used yesterday at 14:02". */
export function relLower(t: string): string {
  return relTime(t).replace(/^(Yesterday|Tomorrow)/, (m) => m.toLowerCase());
}


