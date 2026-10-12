export type Params = Record<string, string>;

export interface Match {
  pattern: string;
  params: Params;
}

// SPEC §9. Patterns never overlap, so resolve order does not matter.
export const patterns = [
  '/setup',
  '/login',
  '/chat',
  '/chat/:id',
  '/approvals',
  '/approvals/:id',
  '/runs/:id',
  '/schedules',
  '/schedules/:id',
  '/memory',
  '/usage',
  '/audit',
  '/settings/:page',
] as const;

export function match(pattern: string, path: string): Params | null {
  const want = pattern.split('/').filter(Boolean);
  const got = path.split('/').filter(Boolean);
  if (want.length !== got.length) return null;
  const params: Params = {};
  for (let i = 0; i < want.length; i++) {
    const w = want[i];
    const g = got[i];
    if (w.startsWith(':')) {
      try {
        params[w.slice(1)] = decodeURIComponent(g);
      } catch {
        return null;
      }
    } else if (w !== g) {
      return null;
    }
  }
  return params;
}

export function resolve(path: string): Match | null {
  for (const pattern of patterns) {
    const params = match(pattern, path);
    if (params) return { pattern, params };
  }
  return null;
}
