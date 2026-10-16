// Sign in with Google and GitHub on Settings → Security (docs/design/ui.md
// §11.6): the shared list of who can sign in, the provider rows and the
// check lines. The server validates again; these only spare a round trip.
import { ApiError } from '../../../lib/api/client';
import type { SignInAdmin, SignInProvider, SignInProviderStatus } from '../../../lib/api/types.gen';

export const providerNames: Record<SignInProvider, string> = { google: 'Google', github: 'GitHub' };

const DOMAIN = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/;
const LOCAL = /^[\x21-\x3f\x41-\x7e]{1,64}$/;
const ORG = /^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$/;

// Mail services that give anyone an address; as a domain they would let strangers in.
const PUBLIC_MAIL = new Set([
  'gmail.com',
  'googlemail.com',
  'outlook.com',
  'hotmail.com',
  'live.com',
  'msn.com',
  'yahoo.com',
  'icloud.com',
  'me.com',
  'mac.com',
  'aol.com',
  'proton.me',
  'protonmail.com',
  'gmx.com',
  'mail.com',
  'yandex.com',
  'qq.com',
]);

/** One entry per line, commas also accepted; lower case, "@acme.com" read as the domain, repeats dropped. */
export function parseList(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(/[\n,]/)) {
    let e = raw.trim().toLowerCase();
    if (e.startsWith('@') && e.indexOf('@', 1) < 0) e = e.slice(1);
    if (e && !out.includes(e)) out.push(e);
  }
  return out;
}

export const listText = (allow: string[]) => allow.join('\n');

/** Why an entry can't go on the list, in plain words, or ''. */
export function entryProblem(e: string): string {
  const at = e.indexOf('@');
  if (at < 0) {
    if (!DOMAIN.test(e)) return `“${e}” isn't an email address or a domain. Write it like acme.com or pat@gmail.com.`;
    if (PUBLIC_MAIL.has(e)) return `Anyone can get an address at ${e}. List each person's address instead.`;
    return '';
  }
  return LOCAL.test(e.slice(0, at)) && DOMAIN.test(e.slice(at + 1)) ? '' : `“${e}” isn't an email address. Write it like pat@gmail.com.`;
}

export function listProblem(list: string[]): string {
  if (list.length > 200) return 'The list can hold up to 200 addresses and domains.';
  for (const e of list) {
    const p = entryProblem(e);
    if (p) return p;
  }
  return '';
}

/** Organizations, separated by commas or lines; repeats dropped whatever their case. */
export function parseOrgs(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(/[\n,]/)) {
    const o = raw.trim();
    if (o && !out.some((x) => x.toLowerCase() === o.toLowerCase())) out.push(o);
  }
  return out;
}

export function orgsProblem(orgs: string[]): string {
  if (orgs.length > 20) return 'Enter up to 20 organizations.';
  return orgs.every((o) => ORG.test(o)) ? '' : 'Enter organization names like acme-inc, separated by commas.';
}

/** Who the provider can let in: the shared list for Google; for GitHub an exact address on it, or an organization. */
export function hasRules(adm: SignInAdmin, st: SignInProviderStatus): boolean {
  if (st.provider === 'github') return adm.allow.some((e) => e.includes('@')) || st.orgs.length > 0;
  return adm.allow.length > 0;
}

/** Everything a provider needs before it can be switched on. */
export function canEnable(adm: SignInAdmin, st: SignInProviderStatus): boolean {
  return adm.address === 'ok' && !!st.client_id && st.secret_saved && hasRules(adm, st);
}

/** The Security page's row value: "Off", "On", or "Not working" in red. */
export function rowValue(st: SignInProviderStatus): { text: string; tone: 'muted' | 'danger' } {
  if (!st.enabled) return { text: 'Off', tone: 'muted' };
  return st.ready ? { text: 'On', tone: 'muted' } : { text: 'Not working', tone: 'danger' };
}

/** "3 addresses and domains", for the provider page's link back to the list. */
export function listSummary(allow: string[]): string {
  if (!allow.length) return 'Nobody yet';
  return allow.length === 1 ? '1 address or domain' : `${allow.length} addresses and domains`;
}

/** The sentence under the list after the server refused it. */
export function listSaveError(e: unknown): string {
  const fields = e instanceof ApiError ? e.fields : [];
  for (const f of fields) {
    const m = /anyone can get an address at (\S+?);/.exec(f.message);
    if (m) return `Anyone can get an address at ${m[1]}. List each person's address instead.`;
    const on = /^auth\.sign_in\.(google|github)\.enabled$/.exec(f.path);
    if (on?.[1] === 'github') return 'GitHub sign-in is on and needs an email address on the list, or an organization. Turn it off first, or keep an address.';
    if (on) return 'Google sign-in is on and needs someone on the list. Turn it off first, or keep at least one entry.';
  }
  return "Couldn't save. Try again.";
}

/** The status line after Save runs the check (§11.6). */
export function checkLine(p: SignInProvider, r: { ok: boolean; code?: string; error?: string }): { tone: 'success' | 'danger' | 'warning'; text: string } {
  const name = providerNames[p];
  if (r.ok) return { tone: 'success', text: `Saved. ${name} accepted the client ID and secret.` };
  if (r.code === 'key_rejected')
    return { tone: 'danger', text: `Saved, but ${name} didn't accept the client ID or secret. Copy them again from ${p === 'google' ? 'Google Cloud' : 'GitHub'}.` };
  if (r.code === 'provider_error' && r.error?.includes('redirect_uri_mismatch'))
    return { tone: 'danger', text: "Saved, but GitHub doesn't accept this callback URL. Paste it into the OAuth app's settings." };
  return { tone: 'warning', text: `Saved, but Bothy couldn't reach ${name} to check the details.` };
}
