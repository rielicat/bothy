// Copy and helpers shared by setup, sign in and the first-password screen
// (docs/design/ui.md §5.3, §5.5, §5.9). Server messages are matched here so
// none of them reaches the screen.
import { ApiError } from '../../lib/api/client';
import type { Me, SetupState } from '../../lib/api/types.gen';
import { parseGoDuration, plural } from '../../lib/format';

// Local until S1 and S9 land in types.gen.ts (docs/design/ui.md §17); C10 moves them.
export type Install = 'compose' | 'docker' | 'binary' | 'unknown';
export type SetupStateV2 = SetupState & { install?: Install };
export type MeV2 = Me & { must_change_password?: boolean };

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
