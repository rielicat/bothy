// Appends the next page, skipping rows already shown: keyset pages can
// overlap when rows share a cursor value or the list changed in between,
// and keyed {#each} blocks throw on a repeated key.
export function appendNew<T>(have: T[], page: T[] | null | undefined, key: (x: T) => string | number): T[] {
  const seen = new Set(have.map(key));
  return [...have, ...(page ?? []).filter((x) => !seen.has(key(x)))];
}
