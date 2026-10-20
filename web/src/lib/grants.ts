import type { Action, GrantCreateReq, PrincipalID } from './api/types.gen';
import { capital, serviceName } from './format';

// core.GrantDomain values are exact "host:port" (IPv6 in brackets); a bare
// host would never match a connection, so the port is always spelled out.
export function hostPort(value: string, port = 443): string {
  const v = value.trim().toLowerCase().replace(/\.$/, '');
  if (/^\[[^\]]+\]:\d+$/.test(v) || /^[^:[\]]+:\d+$/.test(v)) return v;
  if (/^\[[^\]]+\]$/.test(v)) return `${v}:${port}`;
  if (v.includes(':')) return `[${v}]:${port}`;
  return `${v}:${port}`;
}

// siteGrant is "Allow this site" for a blocked box connection. A box can
// upload as well as download, so the egress proxy honours only domain grants
// that reach external_send; a read-class grant would be stored and ignored.
export function siteGrant(principal: PrincipalID, host: string, port: number): GrantCreateReq {
  return { principal_id: principal, kind: 'domain', value: hostPort(host, port), max_se: 'external_send' };
}

// Interpreters run whatever their arguments say, so the gateway pins the whole
// command line for them rather than the program (policy/grants.go).
const interpreters = ['bash', 'sh', 'zsh', 'python', 'python3', 'node', 'env', 'xargs', 'sudo', 'uv', 'npx'];

function payloadOf(a: Pick<Action, 'payload'>): Record<string, unknown> {
  return a.payload && typeof a.payload === 'object' ? (a.payload as Record<string, unknown>) : {};
}

const base = (p: string) => p.split('/').pop() ?? '';

/** The command an exec Action runs: the script of `bash -lc …`, else its argv joined. */
export function commandOf(a: Pick<Action, 'argv' | 'payload'>): string {
  const p = payloadOf(a);
  if (typeof p.command === 'string') return p.command;
  const argv = Array.isArray(p.argv) ? (p.argv as string[]) : (a.argv ?? []);
  if (argv.length === 3 && /^(ba|z)?sh$/.test(base(argv[0])) && argv[1] === '-lc') return argv[2];
  return argv.join(' ');
}

/** "host" for port 443, "host:port" otherwise. */
export function hostOf(a: Pick<Action, 'host' | 'port' | 'url'>): string {
  if (a.host) return a.port && a.port !== 443 && a.port !== 80 ? `${a.host}:${a.port}` : a.host;
  try {
    return a.url ? new URL(a.url).host : '';
  } catch {
    return '';
  }
}

/** Grant wording with identifiers (paths, hosts, commands, repositories) marked for `text-code`. */
export type GrantPart = string | { code: string };

/** The wording for a grant that pins the whole request, used when nothing narrower can be named. */
export const EXACT_GRANT = 'this exact request';

/**
 * What "Allow for this chat" or "Always allow" lets through, in words: the
 * narrow grant the gateway forms from the Action (policy/grants.go
 * narrowGrant). "changes to files in notes/", "contacting api.example.com".
 */
export function describeGrantParts(a: Action | undefined): GrantPart[] {
  if (!a) return [EXACT_GRANT];
  const p = payloadOf(a);
  const code = (c: string) => ({ code: c });
  switch (a.kind) {
    case 'exec': {
      if (typeof p.code === 'string') return ['running this exact script'];
      const argv = Array.isArray(p.argv) ? (p.argv as string[]) : (a.argv ?? []);
      const prog = base(argv[0] ?? '');
      if (!prog) break;
      return interpreters.includes(prog) ? ['running ', code(commandOf(a))] : ['running ', code(prog), ' commands'];
    }
    case 'fs.write':
    case 'fs.delete': {
      const path = (typeof p.path === 'string' ? p.path : (a.paths?.[0] ?? '')).replace(/^\.\//, '');
      if (!path) break;
      const dir = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '';
      if (a.kind === 'fs.delete') return dir ? ['deleting files in ', code(`${dir}/`)] : ['deleting ', code(path)];
      return dir ? ['changes to files in ', code(`${dir}/`)] : ['changes to ', code(path)];
    }
    case 'http':
    case 'net': {
      const host = hostOf(a);
      if (host) return ['contacting ', code(host)];
      break;
    }
    case 'send': {
      const r = a.recipient ?? '';
      if (!r) break;
      if (r.startsWith('github:')) return [`${a.tool === 'repo_open_pr' ? 'pull requests' : 'comments'} in `, code(r.slice(7))];
      const app = r.includes(':') ? r.slice(0, r.indexOf(':')) : '';
      return app ? [`sending messages to this ${capital(app)} chat`] : [`sending messages to ${r}`];
    }
    case 'mcp':
      if (a.recipient) return [`using ${a.mcp_server ? serviceName(a.mcp_server) : 'this tool'} with ${a.recipient}`];
      break;
  }
  return [EXACT_GRANT];
}

/**
 * The words after "Always allow" on the card's link. A command is "this
 * command": printing it again in the link outweighed the buttons, and the
 * dialog the link opens names the grant in full.
 */
export function alwaysLabelParts(a: Action | undefined, grant: GrantPart[] = describeGrantParts(a)): GrantPart[] {
  if (a?.kind === 'exec' && typeof payloadOf(a).code !== 'string') return ['this command'];
  return grant;
}

export function describeGrant(a: Action | undefined): string {
  return describeGrantParts(a)
    .map((x) => (typeof x === 'string' ? x : x.code))
    .join('');
}

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

/** What a stored tool grant lets Bothy do, from its tool: "Change files", "Use create issue in Linear". */
export function toolGrantPhrase(name: string): string {
  if (toolPhrase[name]) return toolPhrase[name];
  if (name.startsWith('schedule_')) return 'Change schedules';
  const mcp = /^mcp__([^_]+(?:_[^_]+)*)__(.+)$/.exec(name);
  if (mcp) return `Use ${mcp[2].replace(/_/g, ' ')} in ${serviceName(mcp[1])}`;
  return `Use ${name.replace(/_/g, ' ')}`;
}
