// Stored grants in plain words for "Allowed without asking" (§11.6).
// lib/grants.ts describes the grant an action would create; this reads the
// Grant records themselves.
import type { Grant, Principal } from '../../../lib/api/types.gen';
import { channelName } from '../../../lib/labels';

const toolPhrase: Record<string, string> = {
  shell: 'Run commands',
  python: 'Run Python scripts',
  fs_write: 'Change files',
  fs_delete: 'Delete files',
  fs_read: 'Read files',
  web_fetch: 'Open web pages',
  web_search: 'Search the web',
  message: 'Send messages',
  memory: 'Update what Bothy remembers',
  net_allow: 'Let running code reach new websites',
};

/** "api.example.com:443" → "api.example.com"; other ports stay. */
export function siteName(value: string): string {
  return value.replace(/:443$/, '');
}

function tool(name: string): string {
  if (toolPhrase[name]) return toolPhrase[name];
  if (name.startsWith('schedule_')) return 'Change schedules';
  const mcp = /^mcp__([^_]+(?:_[^_]+)*)__(.+)$/.exec(name);
  if (mcp) return `Use ${mcp[2].replace(/_/g, ' ')} in ${mcp[1]}`;
  return `Use ${name.replace(/_/g, ' ')}`;
}

export interface GrantParts {
  /** What is allowed, as a sentence fragment: "Send messages to a chat in Slack". */
  what: string;
  /** The exact host, path or pattern the grant names, shown verbatim; empty when the sentence says it all. */
  exact: string;
}

export function describeGrant(g: Grant): GrantParts {
  if (g.kind === 'domain') return { what: 'Reach', exact: siteName(g.value ?? '') };
  if (g.kind === 'recipient') {
    // Chat recipients are ChatRef keys ("slack:C04ENG:"); the chat's name isn't in the grant.
    const [channel] = (g.value ?? '').split(':');
    if (channel && channelName[channel] && (g.value ?? '').includes(':')) return { what: `Send messages to a chat in ${channelName[channel]}`, exact: '' };
    return { what: 'Send messages to', exact: g.value ?? '' };
  }
  const globs = Object.values(g.pattern ?? {});
  return { what: globs.length ? `${tool(g.tool ?? '')} matching` : tool(g.tool ?? ''), exact: globs.join(', ') };
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
