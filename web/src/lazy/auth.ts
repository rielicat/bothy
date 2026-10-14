import type { Match } from '../lib/routes';
import ChangePassword from '../screens/ChangePassword.svelte';
import Login from '../screens/Login.svelte';
import Setup from '../screens/setup/Setup.svelte';
import type { Screen } from '.';

export function screen(m: Match): Screen | null {
  return m.pattern === '/setup' ? Setup : m.pattern === '/login' ? Login : m.pattern === '/password' ? ChangePassword : null;
}
