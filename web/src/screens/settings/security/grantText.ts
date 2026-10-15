// Stored grants in plain words for "Allowed without asking" (§11.6).
// lib/grants.ts describes the grant an action would create; this reads the
// Grant records themselves.
import type { Grant, Principal } from '../../../lib/api/types.gen';
import { commandOf, toolGrantPhrase as tool } from '../../../lib/grants';
import { channelName } from '../../../lib/labels';

/** "api.example.com:443" → "api.example.com"; other ports stay. */
export function siteName(value: string): string {
  return value.replace(/:443$/, '');
}

export interface GrantParts {
  /** What is allowed, as a sentence fragment: "Send messages to a chat in Slack". */
  what: string;
  /** The exact host, path or pattern the grant names, shown verbatim; empty when the sentence says it all. */
  exact: string;
  /** The stored pattern, for Details, when the sentence puts it in words instead of showing it. */
  raw?: string;
}

// The gateway escapes * ? [ and \ in a value it pins exactly (policy/match.go
// globEscape); null when the glob has a live wildcard and so isn't one value.
function literal(glob: string): string | null {
  let out = '';
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i];
    if (c === '\\' && i + 1 < glob.length) out += glob[++i];
    else if ('*?[\\'.includes(c)) return null;
    else out += c;
  }
  return out;
}

function argvOf(glob: string): string[] | null {
  try {
    const v: unknown = JSON.parse(literal(glob) ?? '');
    return Array.isArray(v) && v.length > 0 && v.every((x) => typeof x === 'string') ? v : null;
  } catch {
    return null;
  }
}

export function describeGrant(g: Grant): GrantParts {
  if (g.kind === 'domain') return { what: 'Reach', exact: siteName(g.value ?? '') };
  if (g.kind === 'recipient') {
    // Chat recipients are ChatRef keys ("slack:C04ENG:"); the chat's name isn't in the grant.
    const [channel] = (g.value ?? '').split(':');
    if (channel && channelName[channel] && (g.value ?? '').includes(':')) return { what: `Send messages to a chat in ${channelName[channel]}`, exact: '' };
    if (channel === 'github' && (g.value ?? '').length > 7) return { what: 'Open pull requests or comment in', exact: (g.value ?? '').slice(7) };
    return { what: 'Send messages to', exact: g.value ?? '' };
  }
  const name = g.tool ?? '';
  const pattern = g.pattern ?? {};
  const raw = JSON.stringify({ tool: name, pattern }, null, 2);
  // narrowGrant pins a request it can't narrow by its whole payload, python by
  // its code and an interpreter by its whole argv. Those values mean nothing
  // to a reader, so the line says what they allow and Details keeps them.
  if ('/code' in pattern && literal(pattern['/code']) !== null) return { what: 'Run this exact script', exact: '', raw };
  if ('' in pattern && literal(pattern['']) !== null) return { what: `${tool(name)} with these exact details`, exact: '', raw };
  const argv = '/argv' in pattern ? argvOf(pattern['/argv']) : null;
  if (argv) return { what: 'Run', exact: commandOf({ argv, payload: null }), raw };
  const globs = Object.values(pattern);
  return { what: globs.length ? `${tool(name)} matching` : tool(name), exact: globs.join(', ') };
}

/** The one-line meta: "for Sam Patel · only in “Weekly report” · 3 days ago". */
export function grantMeta(g: Grant, ctx: { principals: Principal[]; titles: Record<string, string>; when: string }): string[] {
  const out: string[] = [];
  const who = ctx.principals.find((p) => p.id === g.principal_id);
  if (who) out.push(who.kind === 'space' ? `in the group chat “${who.display_name}”` : `for ${who.display_name}`);
  if (g.scope === 'session') out.push(ctx.titles[g.scope_id] ? `only in “${ctx.titles[g.scope_id]}”` : 'only in one chat');
  if (g.scope === 'schedule') out.push(ctx.titles[g.scope_id] ? `only for “${ctx.titles[g.scope_id]}”` : 'only for one schedule');
  if (ctx.when) out.push(ctx.when);
  return out;
}
