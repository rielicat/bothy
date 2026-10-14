// Settings groups (docs/design/ui.md §4.3, §4.5): "You" for everyone, the
// workspace groups for admins only.
export interface SettingsGroup {
  id: string;
  label: string;
  /** Which attention dot it carries (§4.10). */
  dot?: 'security' | 'connections';
}

export const personalGroups: SettingsGroup[] = [{ id: 'account', label: 'Account' }];

export const workspaceGroups: SettingsGroup[] = [
  { id: 'general', label: 'General' },
  { id: 'models', label: 'Models' },
  { id: 'connections', label: 'Connections', dot: 'connections' },
  { id: 'people', label: 'People' },
  { id: 'security', label: 'Security', dot: 'security' },
  { id: 'advanced', label: 'Advanced' },
];

/** The page name of an address only admins may open, or null. */
export function adminOnlyTitle(path: string): string | null {
  if (/^\/activity\/audit\/?$/.test(path)) return 'Audit log';
  const g = /^\/settings\/([^/]+)/.exec(path)?.[1];
  return workspaceGroups.find((x) => x.id === g)?.label ?? null;
}

/** True for a /settings/:group address whose group doesn't exist. */
export function unknownGroup(path: string): boolean {
  const g = /^\/settings\/([^/]+)/.exec(path)?.[1];
  return !!g && ![...personalGroups, ...workspaceGroups].some((x) => x.id === g);
}
