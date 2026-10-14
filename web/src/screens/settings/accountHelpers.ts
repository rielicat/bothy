// Plain words for people, passwords and API tokens on Settings → Account and
// People (docs/design/ui.md §11.2, §11.5).
import type { TokenRecord, User } from '../../lib/api/types.gen';
import { isZeroTime, relTime } from '../../lib/format';

// Local until S9 is in types.gen.ts (docs/design/ui.md §17); C10 removes them.
export type UserV2 = User & { last_login_at?: string; must_change_password?: boolean };
export type UserCreateResp = UserV2 & { one_time_password?: string };

/** The signed-in person's own profile (server need: self-service name and approval delivery). */
export interface Profile {
  display_name: string;
  /** Where approval requests reach this person: "web" or a linked chat app. */
  approvals_in: string;
}

export const USERNAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/;

export function usernameError(u: string): string {
  return USERNAME.test(u) ? '' : 'Use up to 32 letters, numbers, dots, dashes or underscores, starting with a letter or number.';
}

export const MIN_PASSWORD = 12;

/** Live help under a new password, the same words as setup (§5.5). */
export function passwordHelp(pw: string): string {
  const n = [...pw].length;
  if (!n) return 'At least 12 characters.';
  if (n >= MIN_PASSWORD) return 'Long enough.';
  const left = MIN_PASSWORD - n;
  return `${left} more ${left === 1 ? 'character' : 'characters'} needed.`;
}

// No 0/O, 1/l/I: the password is read off a screen and typed by someone else.
const ALPHABET = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';

/** A one-time password in four groups of four, "h7Fq-2mKx-9wPr-tB4n". */
export function generatePassword(random: (n: number) => Uint32Array = (n) => crypto.getRandomValues(new Uint32Array(n))): string {
  const r = random(16);
  const chars = Array.from(r, (v) => ALPHABET[v % ALPHABET.length]);
  return [0, 4, 8, 12].map((i) => chars.slice(i, i + 4).join('')).join('-');
}

export interface ScopeChoice {
  value: string;
  label: string;
  /** Short form for the token's summary line. */
  verb: string;
}

export function scopeChoices(admin: boolean): ScopeChoice[] {
  return [
    { value: 'chat', label: 'Chat with Bothy', verb: 'chat' },
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

/** "Member", "Member · hasn't signed in yet", "Admin · Turned off". */
export function roleLine(u: UserV2): string {
  const parts = [roleWord[u.role] ?? u.role];
  if (u.disabled) parts.push('Turned off');
  // Only a server that reports sign-ins (S9) can say nobody has signed in.
  else if ('last_login_at' in u && isZeroTime(u.last_login_at)) parts.push("hasn't signed in yet");
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
