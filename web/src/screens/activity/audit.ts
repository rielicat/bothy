// Plain lines for the audit log (docs/design/ui.md §10.6). The server will
// send a one-line summary and the actor's name per row (S16, S8); until then
// they are built here from the action, its target and the actor kind.
import { appName } from '../../lib/taskStatus';
import { personName, type AuditRow, type EgressRow } from './api';

export function actorName(actor: string, names: Map<string, string>, given?: string): string {
  const [kind, id = ''] = actor.split(':');
  if (kind === 'user') return personName(id, names, given);
  if (given) return given;
  if (kind === 'channel') return `Someone on ${appName(id)}`;
  if (kind === 'token') return 'An API token';
  return kind === 'schedule' ? 'Schedule' : 'Bothy';
}

const tools: Record<string, string> = {
  web_search: 'Searched the web',
  web_fetch: 'Opened a website',
  fs_read: 'Read a file',
  fs_list: 'Looked in a folder',
  fs_write: 'Changed a file',
  fs_delete: 'Deleted a file',
  shell: 'Ran a command',
  python: 'Ran a Python script',
  message: 'Sent a message',
  memory: 'Remembered something',
  repo_checkout: 'Got a copy of a repository',
  repo_open_pr: 'Opened a pull request',
  github_comment: 'Commented on GitHub',
  release_deploy: 'Started a deploy',
};

const actions: Record<string, string> = {
  'auth.setup_claim': 'Opened setup',
  'auth.owner_created': 'Created the owner account',
  'auth.login': 'Signed in',
  'auth.login_failed': "Couldn't sign in",
  'auth.lockout': 'Sign-in locked after too many tries',
  'auth.logout': 'Signed out',
  'auth.password_changed': 'Changed their password',
  'token.create': 'Created an API token',
  'token.revoke': 'Revoked an API token',
  'user.create': 'Added a person',
  'user.update': "Changed a person's account",
  'pairing.create': 'Started linking a chat app',
  'pairing.redeem': 'Linked a chat app',
  'pairing.burn': 'Cancelled a chat link code that was posted in a group',
  'identity.link': 'Linked a chat app account',
  'identity.guest': 'Let someone chat as a guest',
  'identity.block': 'Blocked someone in a chat app',
  'space.update': 'Changed a group chat',
  'config.set': 'Changed settings',
  'config.reset': 'Reset a setting',
  'persona.update': "Changed Bothy's personality",
  'vault.put': 'Saved a key',
  'vault.delete': 'Removed a saved key',
  'vault.rotate': 'Replaced the key that protects saved keys',
  'vault.unseal': 'Unlocked saved keys',
  'secret.use': 'Used a saved key',
  'tool.denied': 'Was blocked from an action',
  'approval.request': 'Asked for approval',
  'approval.expire': 'A request expired',
  'grant.create': 'Allowed something without asking',
  'grant.revoke': 'Removed a permission',
  'memory.write': 'Remembered something',
  'memory.edit': 'Edited what Bothy remembers',
  'skill.install': 'Added a skill',
  'skill.enable': 'Turned on a skill',
  'skill.disable': 'Turned off a skill',
  'skill.changed': 'A skill changed',
  'mcp.install': 'Added a connected tool',
  'mcp.approve': 'Approved connected tools',
  'mcp.changed': 'Connected tools changed',
  'schedule.create': 'Created a schedule',
  'schedule.update': 'Changed a schedule',
  'schedule.delete': 'Deleted a schedule',
  'schedule.run': 'Started a schedule by hand',
  'run.stop': 'Stopped a task',
  'budget.exceeded': 'Hit the spending limit',
  'admin.pause': 'Paused Bothy',
  'admin.resume': 'Resumed Bothy',
  'admin.restart': 'Restarted Bothy',
  'backup.create': 'Made a backup',
  'sandbox.selftest_failed': 'Running code failed its self-check',
  'api.passthrough': 'Answered an API request',
  'audit.archived': 'Archived old log entries',
};

export function summarize(e: AuditRow): string {
  if (e.summary) return e.summary;
  const detail = (e.detail ?? {}) as Record<string, unknown>;
  switch (e.action) {
    case 'tool.exec':
      return tools[e.target] ?? 'Took an action';
    case 'approval.decide':
      return detail.choice === 'deny' ? 'Denied a request' : 'Approved a request';
    case 'egress.allow':
      return e.target ? `Reached ${e.target}` : 'Reached a website';
    case 'egress.deny':
      return e.target ? `Blocked from sending to ${e.target}` : 'Was blocked from a website';
  }
  return actions[e.action] ?? 'Recorded an event';
}

/** The word shown only when something didn't succeed. */
export function resultWord(e: AuditRow): string {
  if (e.action === 'egress.deny' || e.action === 'tool.denied' || e.outcome === 'denied') return 'Blocked';
  return e.outcome === 'error' ? 'Failed' : '';
}

export const actionGroups: { value: string; label: string; prefixes: string[] }[] = [
  { value: '', label: 'Any action', prefixes: [] },
  { value: 'signin', label: 'Signing in', prefixes: ['auth.'] },
  { value: 'tasks', label: 'What Bothy did', prefixes: ['tool.', 'run.', 'api.', 'budget.'] },
  { value: 'approvals', label: 'Approvals and permissions', prefixes: ['approval.', 'grant.'] },
  { value: 'websites', label: 'Websites', prefixes: ['egress.'] },
  { value: 'people', label: 'People and chat apps', prefixes: ['user.', 'token.', 'pairing.', 'identity.', 'space.'] },
  { value: 'settings', label: 'Settings', prefixes: ['config.', 'persona.', 'admin.', 'backup.', 'sandbox.', 'audit.'] },
  { value: 'keys', label: 'Saved keys', prefixes: ['vault.', 'secret.'] },
  { value: 'knowledge', label: 'Memory, skills and tools', prefixes: ['memory.', 'skill.', 'mcp.'] },
  { value: 'schedules', label: 'Schedules', prefixes: ['schedule.'] },
];

export type WebOutcome = 'reached' | 'allowed_unreachable' | 'blocked';

/** One merged outcome per connection: what the proxy decided and whether the site answered. */
export function webOutcome(e: EgressRow): WebOutcome {
  if (e.outcome) return e.outcome;
  if (e.decision.includes('deny')) return 'blocked';
  return e.bytes_down > 0 ? 'reached' : 'allowed_unreachable';
}

export function webLine(e: EgressRow): string {
  const o = webOutcome(e);
  if (o === 'blocked') return `Blocked: ${e.host} isn't on the allowed list`;
  if (o === 'allowed_unreachable') return `Allowed, but ${e.host} didn't answer`;
  return `Reached ${e.host}`;
}
