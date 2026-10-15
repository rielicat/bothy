import { describe, expect, it } from 'vitest';
import { eventTypes } from './sse.svelte';

// The EventSource only delivers the types it subscribed to, so a screen that
// listens for one missing here never hears it (Knowledge never updated live).
const SOURCES = import.meta.glob<string>(['../**/*.svelte', '../**/*.ts', '!../**/*.test.ts'], { query: '?raw', import: 'default', eager: true });

describe('SSE subscriptions', () => {
  it('subscribe to every event type a screen listens for', () => {
    const listened = new Set<string>();
    for (const src of Object.values(SOURCES)) for (const m of src.matchAll(/events\.on\('([a-z_.]+)'/g)) listened.add(m[1]);
    expect(listened.size).toBeGreaterThan(5);
    expect([...listened].filter((t) => !(eventTypes as string[]).includes(t))).toEqual([]);
  });
});
