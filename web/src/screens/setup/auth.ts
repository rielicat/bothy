// Copy and helpers shared by setup, sign in and the first-password screen
// (docs/design/ui.md §5.3, §5.5, §5.9). Server messages are matched here so
// none of them reaches the screen.
import { ApiError } from '../../lib/api/client';
import type { InstallKind } from '../../lib/api/types.gen';
import { parseGoDuration, plural } from '../../lib/format';

export type Install = InstallKind;

export const NO_RESPONSE = "Bothy didn't respond. Check that it's still running, then try again.";

/** A request that never got an answer from Bothy itself (fetch failed, or a proxy answered for it). */
export const unanswered = (e: unknown) => !(e instanceof ApiError) || e.status >= 502;

export const commands: Record<Exclude<Install, 'unknown'>, { label: string; cmd: string }> = {
  compose: { label: 'Docker Compose', cmd: 'docker compose exec bothy /bothy setup-token' },
  docker: { label: 'Docker', cmd: 'docker exec bothy /bothy setup-token' },
  binary: { label: 'Installed directly', cmd: 'bothy setup-token' },
};

const others: Record<Exclude<Install, 'unknown'>, string> = {
  compose: 'Not using Docker Compose?',
  docker: 'Not using Docker?',
  binary: 'Running Bothy in Docker?',
};

/** The command for this install first, and the other two behind a disclosure (§5.3). */
export function setupCommands(install: Install | undefined) {
  const main = install && install !== 'unknown' ? install : 'compose';
  return { main: commands[main].cmd, more: others[main], rest: (Object.keys(commands) as (keyof typeof commands)[]).filter((k) => k !== main).map((k) => commands[k]) };
}

export function codeError(e: unknown): string {
  if (unanswered(e)) return NO_RESPONSE;
  if ((e as ApiError).status === 429) return 'Too many tries. Wait a minute, then try again.';
  return "That code didn't work. It may have expired, so run the command below to get a new one.";
}

export type AccountField = 'name' | 'username' | 'password';

/** Where an owner-creation failure belongs: a field, the form, or back to the setup code. */
export function ownerError(e: unknown): { field?: AccountField; text: string; expired?: boolean } {
  if (unanswered(e)) return { text: NO_RESPONSE };
  const err = e as ApiError;
  if (err.status === 401) return { text: 'Setup timed out. Paste the setup code again.', expired: true };
  const m = err.message.toLowerCase();
  if (err.status === 400 || err.status === 422) {
    if (m.includes('common') || m.includes('predictable')) return { field: 'password', text: 'This password is too common. Try a phrase of a few words.' };
    if (m.includes('password')) return { field: 'password', text: 'Use at least 12 characters.' };
    if (m.includes('username')) return { field: 'username', text: 'Use up to 32 letters, numbers, dots, dashes or underscores, starting with a letter or number.' };
    if (m.includes('display name')) return { field: 'name', text: 'Enter your name.' };
  }
  return { text: NO_RESPONSE };
}

export function loginError(e: unknown): string {
  if (unanswered(e)) return "Bothy didn't respond. Check your connection, then try again.";
  const err = e as ApiError;
  const m = err.message.toLowerCase();
  if (err.status === 429) {
    const after = parseGoDuration(/try again in (\S+)/.exec(m)?.[1] ?? '');
    return after ? `Too many tries. Try again in ${plural(Math.max(1, Math.ceil(after / 60e9)), 'minute')}.` : 'Too many tries. Wait a minute, then try again.';
  }
  if (m.includes('disabled') || m.includes('turned off')) return 'This account is turned off. Ask an admin to turn it back on.';
  if (err.status === 401) return "That username and password don't match.";
  return "Bothy didn't respond. Check your connection, then try again.";
}

/** "Google" or "GitHub" for a provider id from the URL, else ''. */
export function providerName(p: string | null | undefined): string {
  return p === 'google' ? 'Google' : p === 'github' ? 'GitHub' : '';
}

const REFUSED = ['__Host-bothy_signin_who', 'bothy_signin_who'];

/**
 * The account a provider just refused, which the server leaves in a cookie
 * for a minute instead of the address, so a crafted link can't name anyone.
 * Read once: the cookie is deleted here.
 */
export function takeRefused(): string {
  let who = '';
  for (const name of REFUSED) {
    const raw = document.cookie
      .split(';')
      .map((c) => c.trim())
      .find((c) => c.startsWith(`${name}=`));
    if (raw === undefined) continue;
    document.cookie = `${name}=; Max-Age=0; Path=/; SameSite=Lax${name.startsWith('__Host-') ? '; Secure' : ''}`;
    try {
      who ||= decodeURIComponent(raw.slice(name.length + 1));
    } catch {
      // a mangled value falls back to the general sentence
    }
  }
  return who;
}

// The account comes from a cookie the page could not have set itself, but
// anything else than an address or a login is still left out of the sentence.
function account(who: string | null | undefined, provider: string | null | undefined): string {
  if (!who || who.length > 100) return '';
  if (/^[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$/.test(who)) return who;
  return provider === 'github' && /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/.test(who) ? `@${who}` : '';
}

function notListed(provider: string | null | undefined, who: string | null | undefined): string {
  const a = account(who, provider);
  const p = providerName(provider);
  return `${a || (p ? `That ${p} account` : 'That account')} isn't on this Bothy's sign-in list. Ask an admin to add you.`;
}

/** The sign-in page's sentence for a provider sign-in that came back without signing in (§5.9). */
export function signInErrorText(code: string, provider: string | null | undefined, who?: string | null): string {
  const p = providerName(provider);
  switch (code) {
    case 'cancelled':
      return p ? `Signing in with ${p} was cancelled.` : 'Signing in was cancelled.';
    case 'expired':
      return 'That sign-in took too long or was started in another tab. Try again.';
    case 'not_allowed':
      return notListed(provider, who);
    case 'org_access':
      return "Your GitHub organization hasn't approved Bothy yet. Ask an organization owner to approve it, then try again.";
    case 'no_account':
      return `No Bothy account uses that ${p || 'sign-in'} account yet. Sign in with your password and connect it in Account, or ask an admin.`;
    case 'disabled':
      return 'This account is turned off. Ask an admin to turn it back on.';
  }
  return `${p ? `${p} sign-in` : 'That sign-in'} isn't working right now. Sign in with your username and password, or ask an admin.`;
}

/** Account → Sign-in methods' sentence for a connection that didn't finish (§11.2). */
export function connectErrorText(code: string, provider: string | null | undefined, who?: string | null): string {
  const p = providerName(provider) || 'it';
  switch (code) {
    case 'taken':
      return `That ${p} account is already connected to someone else here.`;
    case 'already_linked':
      return `You've already connected a ${p} account. Disconnect it first.`;
    case 'cancelled':
      return `You didn't finish connecting ${p}.`;
    case 'expired':
      return 'Connecting took too long. Try again.';
    case 'signed_out':
      return `You were signed out before ${p} answered. Sign in and try again.`;
    case 'not_allowed':
    case 'org_access':
      return signInErrorText(code, provider, who);
  }
  return `${p} didn't respond. Try again, or ask an admin to check ${p} sign-in.`;
}

const DEFAULT_TUNNEL = 'ssh -L 8080:127.0.0.1:8080 you@your-server';

/** The tunnel command from the server's 403 fix line, verbatim, and the address to open after it (§5.4). */
export function tunnel(fix: string | undefined): { cmd: string; url: string } {
  const cmd = /^ssh\s[^,]+/.exec(fix?.trim() ?? '')?.[0].trim() ?? DEFAULT_TUNNEL;
  const port = /-L\s*(\d+):/.exec(cmd)?.[1] ?? '8080';
  return { cmd, url: `http://localhost:${port}/setup` };
}

// The one-time password a person just signed in with: /password sends it as
// the old one. Memory only, so a reload asks for it again.
let oneTime = '';
export const rememberOneTime = (pw: string) => (oneTime = pw);
export function takeOneTime(): string {
  const v = oneTime;
  oneTime = '';
  return v;
}

/** The browser's time zone, which setup applies without asking (§5.8). */
export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}
