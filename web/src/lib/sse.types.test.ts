import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { eventTypes } from './sse.svelte';

// The EventSource only delivers the types it subscribed to, so a screen that
// listens for one missing here never hears it (Knowledge never updated live).
function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    if (statSync(p).isDirectory()) return sources(p);
    return /\.(svelte|ts)$/.test(n) && !/\.test\.ts$/.test(n) ? [p] : [];
  });
}

describe('SSE subscriptions', () => {
  it('subscribe to every event type a screen listens for', () => {
    const listened = new Set<string>();
    for (const f of sources(join(__dirname, '..'))) {
      for (const m of readFileSync(f, 'utf8').matchAll(/events\.on\('([a-z_.]+)'/g)) if (m[1] !== '*') listened.add(m[1]);
    }
    expect([...listened].filter((t) => !eventTypes.includes(t as (typeof eventTypes)[number]))).toEqual([]);
  });
});
