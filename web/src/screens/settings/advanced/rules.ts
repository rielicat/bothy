// Policy rules for Advanced → Rules (§11.7): read from the dotted config
// entries, written back as one object per rule, and the tester's answer in a
// sentence.
import type { Decision, SideEffect } from '../../../lib/api/types.gen';

export type Verdict = 'auto' | 'ask' | 'deny';

export interface Rule {
  id: string;
  tool: string;
  argv0: string[];
  domains: string[];
  paths: string[];
  match: Record<string, string>;
  verdict: Verdict;
  reason: string;
  /** Fields the form doesn't edit, written back unchanged. */
  side_effect: string;
  triggers: string[];
}

export const verdictLabel: Record<Verdict, string> = { auto: 'Allows', ask: 'Asks first', deny: 'Denies' };

const list = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : []);

/** Rebuilds a rule from config.value lookups of `policy.rules.<id>.<field>`. */
export function readRule(id: string, get: (field: string) => unknown): Rule {
  const verdict = String(get('verdict') ?? 'ask');
  const match = get('match');
  return {
    id,
    tool: String(get('tool') ?? ''),
    argv0: list(get('argv0')),
    domains: list(get('domains')),
    paths: list(get('paths')),
    match: match && typeof match === 'object' && !Array.isArray(match) ? (match as Record<string, string>) : {},
    verdict: verdict === 'auto' || verdict === 'deny' ? verdict : 'ask',
    reason: String(get('reason') ?? ''),
    side_effect: String(get('side_effect') ?? ''),
    triggers: list(get('triggers')),
  };
}

/**
 * Match conditions are stored one entry per pointer (`policy.rules.x.match./argv/2`),
 * so the object is gathered from the keys under the rule.
 */
export function matchesOf(id: string, paths: string[], get: (path: string) => unknown): Record<string, string> {
  const prefix = `policy.rules.${id}.match.`;
  const out: Record<string, string> = {};
  for (const p of paths) if (p.startsWith(prefix)) out[p.slice(prefix.length)] = String(get(p) ?? '');
  return out;
}

/** The "When" column: the action and its conditions, verbatim. */
export function whenParts(r: Rule): string[] {
  const out: string[] = [];
  if (r.argv0.length) out.push(`commands ${r.argv0.join(', ')}`);
  if (r.domains.length) out.push(`sites ${r.domains.join(', ')}`);
  if (r.paths.length) out.push(`files ${r.paths.join(', ')}`);
  for (const [k, v] of Object.entries(r.match)) out.push(`${k} matches ${v}`);
  return out;
}

export const splitList = (s: string): string[] =>
  s
    .split(/[,\n]/)
    .map((x) => x.trim())
    .filter(Boolean);

export const validRuleId = (s: string) => /^[a-z0-9][a-z0-9-]*$/.test(s);

const seVerb: Record<SideEffect, string> = {
  read: 'only read',
  local_write: 'change files',
  external_send: 'send information out',
  irreversible: "can't be undone",
  financial: 'spend money',
  admin: 'change settings',
};

/** "Bothy would ask, because rule wiki-writes matches." */
export function verdictSentence(d: Pick<Decision, 'verdict' | 'rule' | 'reason'>): string {
  const what = d.verdict === 'auto' ? 'Bothy would do it without asking' : d.verdict === 'deny' ? 'Bothy would refuse' : 'Bothy would ask';
  const i = d.rule.indexOf(':');
  const kind = i < 0 ? d.rule : d.rule.slice(0, i);
  const rest = i < 0 ? '' : d.rule.slice(i + 1);
  let why = '';
  if (kind === 'rule') why = `rule ${rest} matches`;
  else if (kind === 'grant') why = 'a permission allows it';
  else if (kind === 'floor') why = 'a built-in safety rule applies';
  else if (kind === 'first_contact') why = `it's the first time Bothy contacts this ${rest === 'domain' ? 'website' : 'recipient'}`;
  else if (kind === 'default' && rest in seVerb) why = `of the approval setting for actions that ${seVerb[rest as SideEffect]}`;
  else return d.reason ? `${what}: ${d.reason.replace(/\.$/, '')}.` : `${what}.`;
  return `${what}, because ${why}.`;
}
