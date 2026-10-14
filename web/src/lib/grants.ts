import type { Action, GrantCreateReq, PrincipalID } from './api/types.gen';

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

const capital = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s);

/**
 * What "Allow for this chat" or "Always allow" lets through, in words: the
 * narrow grant the gateway forms from the Action (policy/grants.go
 * narrowGrant). "changes to files in notes/", "contacting api.example.com".
 */
export function describeGrant(a: Action | undefined): string {
  if (!a) return 'this exact request';
  const p = payloadOf(a);
  switch (a.kind) {
    case 'exec': {
      if (typeof p.code === 'string') return 'running this exact script';
      const argv = Array.isArray(p.argv) ? (p.argv as string[]) : (a.argv ?? []);
      const prog = base(argv[0] ?? '');
      if (!prog) break;
      return interpreters.includes(prog) ? `running ${commandOf(a)}` : `running ${prog} commands`;
    }
    case 'fs.write':
    case 'fs.delete': {
      const path = (typeof p.path === 'string' ? p.path : (a.paths?.[0] ?? '')).replace(/^\.\//, '');
      if (!path) break;
      const dir = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '';
      if (a.kind === 'fs.delete') return dir ? `deleting files in ${dir}/` : `deleting ${path}`;
      return dir ? `changes to files in ${dir}/` : `changes to ${path}`;
    }
    case 'http':
    case 'net': {
      const host = hostOf(a);
      if (host) return `contacting ${host}`;
      break;
    }
    case 'send': {
      const r = a.recipient ?? '';
      if (!r) break;
      if (r.startsWith('github:')) return `${a.tool === 'repo_open_pr' ? 'pull requests' : 'comments'} in ${r.slice(7)}`;
      const app = r.includes(':') ? r.slice(0, r.indexOf(':')) : '';
      return app ? `sending messages to this ${capital(app)} chat` : `sending messages to ${r}`;
    }
    case 'mcp':
      if (a.recipient) return `using ${capital(a.mcp_server ?? 'this tool')} with ${a.recipient}`;
      break;
  }
  return 'this exact request';
}
