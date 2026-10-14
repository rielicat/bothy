// The shell's one polite live region (§16): decisions, saves, copies and
// connection state. LiveRegion.svelte renders it; announce() writes to it.

class Live {
  polite = $state('');
  assertive = $state('');
  /** Mounted regions; only the first speaks, so nothing is announced twice. */
  hosts: symbol[] = $state([]);
}

export const live = new Live();

/**
 * Reads `text` to screen readers. Repeating the same message still announces
 * it: a trailing no-break space makes the region's content change.
 */
export function announce(text: string, opts: { assertive?: boolean } = {}) {
  const key = opts.assertive ? 'assertive' : 'polite';
  live[key] = live[key] === text ? `${text} ` : text;
}
