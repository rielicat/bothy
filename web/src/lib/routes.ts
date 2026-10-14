export type Params = Record<string, string>;

export interface Match {
  pattern: string;
  params: Params;
}

// docs/design/ui.md §4.5. Patterns never overlap, so resolve order does not matter.
export const patterns = [
  '/setup',
  '/login',
  '/password',
  '/chat',
  '/chat/:id',
  '/approvals',
  '/approvals/:id',
  '/knowledge',
  '/knowledge/search',
  '/knowledge/memory',
  '/knowledge/skills',
  '/knowledge/skills/:name',
  '/knowledge/projects/:owner/:repo',
  '/activity',
  '/activity/tasks/:id',
  '/activity/schedules',
  '/activity/schedules/:id',
  '/activity/releases',
  '/activity/spending',
  '/activity/audit',
  '/settings',
  '/settings/:group',
  '/settings/:group/:item',
  ...(import.meta.env.DEV ? ['/dev/ui'] : []),
] as const;

// v0.1 addresses still resolve, so App can send them on with redirect();
// links already sent to chat apps and bookmarks keep working.
export const legacyPatterns = ['/runs/:id', '/schedules', '/schedules/:id', '/memory', '/usage', '/audit'] as const;

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
  for (const pattern of [...patterns, ...legacyPatterns]) {
    const params = match(pattern, path);
    if (params) return { pattern, params };
  }
  return null;
}

// Old → new (§4.5). `:x` copies that raw segment, so escapes survive untouched.
const redirects: [string, string][] = [
  ['/runs/:id', '/activity/tasks/:id'],
  ['/schedules', '/activity/schedules'],
  ['/schedules/:id', '/activity/schedules/:id'],
  ['/memory', '/knowledge/memory'],
  ['/usage', '/activity/spending'],
  ['/audit', '/activity/audit'],
  ['/settings/channels', '/settings/connections'],
  ['/settings/mcp', '/settings/connections'],
  ['/settings/policy', '/settings/security'],
  ['/settings/sandbox', '/settings/security'],
  ['/settings/vault', '/settings/security/keys'],
  ['/settings/tokens', '/settings/account'],
  ['/settings/persona', '/settings/general'],
  ['/settings/budgets', '/settings/general'],
  ['/settings/skills', '/knowledge/skills'],
];

/** The new path for a v0.1 path, or null. Query and hash are the caller's to keep. */
export function redirect(path: string): string | null {
  const got = path.split('/').filter(Boolean);
  for (const [from, to] of redirects) {
    const want = from.split('/').filter(Boolean);
    if (want.length !== got.length) continue;
    const raw: Params = {};
    let same = true;
    want.forEach((w, i) => {
      if (w.startsWith(':')) raw[w.slice(1)] = got[i];
      else if (w !== got[i]) same = false;
    });
    if (same) return to.replace(/:(\w+)/g, (_, k: string) => raw[k]);
  }
  return null;
}

/** Where /settings lands on a wide screen (§4.5); phones and tablets get the group list. */
export function settingsHome(admin: boolean): string {
  return admin ? '/settings/general' : '/settings/account';
}
