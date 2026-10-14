import SettingsIndex from '../components/shell/SettingsIndex.svelte';
import type { Match } from '../lib/routes';
import Advanced from '../screens/settings/advanced/Advanced.svelte';
import { connectionsScreen } from '../screens/settings/connections';
import Permissions from '../screens/settings/security/Permissions.svelte';
import SavedKeys from '../screens/settings/security/SavedKeys.svelte';
import Security from '../screens/settings/security/Security.svelte';
import Websites from '../screens/settings/security/Websites.svelte';
import Settings from '../screens/settings/Settings.svelte';
import type { Screen } from '.';

const security: Partial<Record<string, Screen>> = { permissions: Permissions, websites: Websites, keys: SavedKeys };

export function screen(m: Match): Screen | null {
  const p = m.params;
  switch (m.pattern) {
    case '/settings':
      return SettingsIndex;
    case '/settings/:group':
      if (p.group === 'connections') return connectionsScreen();
      if (p.group === 'security') return Security;
      if (p.group === 'advanced') return Advanced;
      return Settings;
    case '/settings/:group/:item':
      if (p.group === 'connections') return connectionsScreen(p.item);
      if (p.group === 'security') return security[p.item] ?? null;
      return p.group === 'general' && p.item === 'personality' ? Settings : null;
  }
  return null;
}
