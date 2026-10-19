// Plain words for people, passwords and API tokens on Settings → Account and
// People (docs/design/ui.md §11.2, §11.5).
import type { TokenRecord, User } from '../../lib/api/types.gen';
import { isZeroTime, relTime } from '../../lib/format';

export interface ScopeChoice {
  value: string;
  label: string;
  /** Short form for the token's summary line. */
  verb: string;
  hint?: string;
}

export function scopeChoices(admin: boolean): ScopeChoice[] {
  return [
    { value: 'chat', label: 'Chat with Bothy', verb: 'chat', hint: 'It can also change or delete your chats, memory, schedules and saved keys.' },
    { value: 'read', label: 'Read your chats and activity', verb: 'read' },
    { value: 'llm', label: 'Use the AI models directly', verb: 'use the AI models' },
    ...(admin ? [{ value: 'admin', label: 'Change settings', verb: 'change settings' }] : []),
  ];
}

/** "Can chat and read", "Can chat, read and use the AI models". */
export function scopeSummary(scopes: string[]): string {
  const verbs = scopeChoices(true)
    .filter((c) => scopes.includes(c.value))
    .map((c) => c.verb);
  if (!verbs.length) return 'Can do nothing yet';
  const list = verbs.length === 1 ? verbs[0] : `${verbs.slice(0, -1).join(', ')} and ${verbs[verbs.length - 1]}`;
  return `Can ${list}`;
}

export function lastUsed(t: TokenRecord, now = Date.now()): string {
  return isZeroTime(t.last_used_at) ? 'Not used yet' : `Last used ${relTime(t.last_used_at, { now })}`;
}

export function expired(t: TokenRecord, now = Date.now()): boolean {
  return !isZeroTime(t.expires_at) && new Date(t.expires_at).getTime() <= now;
}

export const roleWord: Record<string, string> = { owner: 'Owner', admin: 'Admin', member: 'Member' };

export function displayName(u: Pick<User, 'display_name' | 'username'>): string {
  return u.display_name || u.username;
}

/** "Member", "Member · hasn't signed in yet", "Admin · Turned off", "Owner · You". */
export function roleLine(u: Pick<User, 'role' | 'disabled' | 'last_login_at'>, you = false): string {
  const parts = [roleWord[u.role] ?? u.role];
  // A session from before sign-in times were kept has none; the viewer is signed in either way.
  if (you) parts.push('You');
  else if (u.disabled) parts.push('Turned off');
  else if (isZeroTime(u.last_login_at)) parts.push("hasn't signed in yet");
  return parts.join(' · ');
}

/** The money input's text for a dollar amount: "5.00". */
export function moneyText(v: unknown): string {
  return typeof v === 'number' && Number.isFinite(v) ? v.toFixed(2) : '';
}

/** Parses "5", "$5.00" or "1,200.5"; null when it isn't an amount of 0 or more. */
export function parseMoney(s: string): number | null {
  const t = s.trim().replace(/^\$/, '').replace(/,/g, '');
  if (!/^\d+(\.\d{0,2})?$|^\.\d{1,2}$/.test(t)) return null;
  return Math.round(Number(t) * 100) / 100;
}
