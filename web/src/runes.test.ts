import { compile } from 'svelte/compiler';
import { expect, it } from 'vitest';

const sources = import.meta.glob<string>('./**/*.svelte', { query: '?raw', import: 'default', eager: true });

// One component without a rune compiles in legacy mode, which turns on Svelte's
// legacy runtime for the whole app: about 500 B gz on the first screen.
it('compiles every component in runes mode', () => {
  const legacy = Object.entries(sources)
    .filter(([filename, source]) => !compile(source, { filename, generate: false }).metadata.runes)
    .map(([filename]) => filename);
  expect(legacy, 'add <svelte:options runes /> to a component that uses no rune').toEqual([]);
});
