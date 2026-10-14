import type { Match } from '../lib/routes';
import Approvals from '../screens/Approvals.svelte';
import type { Screen } from '.';

export function screen(m: Match): Screen | null {
  return m.pattern === '/approvals' || m.pattern === '/approvals/:id' ? Approvals : null;
}
