// Settings groups (docs/design/ui.md §4.3, §4.5): "You" for everyone, the
// workspace groups for admins only.
import type { IconName } from '../../lib/icons';

export interface SettingsGroup {
  id: string;
  label: string;
  icon: IconName;
  /** Which attention dot it carries (§4.10). */
  dot?: 'security' | 'connections';
}

export const personalGroups: SettingsGroup[] = [{ id: 'account', label: 'Account', icon: 'user' }];

export const workspaceGroups: SettingsGroup[] = [
  { id: 'general', label: 'General', icon: 'sliders' },
  { id: 'models', label: 'Models', icon: 'models' },
  { id: 'connections', label: 'Connections', icon: 'plug', dot: 'connections' },
  { id: 'people', label: 'People', icon: 'users' },
  { id: 'security', label: 'Security', icon: 'shield', dot: 'security' },
  { id: 'advanced', label: 'Advanced', icon: 'code' },
];

/** The groups under their headings: "You", then "Workspace" for admins. */
export const settingsSections = (admin: boolean) => [
  { key: 'you', label: 'You', groups: personalGroups },
  ...(admin ? [{ key: 'workspace', label: 'Workspace', groups: workspaceGroups }] : []),
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
