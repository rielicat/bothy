// Websites running code may reach (egress.allow, §11.6): what a person types
// becomes an allowlist entry, and recent blocks are matched against the list.
import type { EgressRecord } from '../../../lib/api/types.gen';

/** "https://Docs.Example.com/guide" → "docs.example.com"; null when it isn't a site. */
export function siteEntry(typed: string): string | null {
  let v = typed.trim().toLowerCase();
  v = v.replace(/^[a-z][a-z0-9+.-]*:\/\//, '').replace(/[/?#].*$/, '').replace(/\.$/, '');
  if (/^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9-]{2,}(:\d{1,5})?$/.test(v)) return v.replace(/:443$/, '');
  if (/^\d{1,3}(\.\d{1,3}){3}(:\d{1,5})?$/.test(v)) return v.replace(/:443$/, '');
  return null;
}

/** "pypi.org" for port 443, "registry.example.com:8443" otherwise. */
export function siteOf(host: string, port: number): string {
  return port === 443 || !port ? host : `${host}:${port}`;
}

/** Whether an allowlist entry covers a host and port, the way the proxy matches them. */
export function covers(entry: string, host: string, port: number): boolean {
  const [name, p] = entry.split(/:(?=\d+$)/);
  if (p ? Number(p) !== port : port !== 443) return false;
  if (name.startsWith('*.')) return host.endsWith(name.slice(1));
  return host === name;
}

/** Recent blocked connections from running code, one row per site, newest first. */
export function recentBlocks(records: EgressRecord[], allow: string[], max = 8): EgressRecord[] {
  const seen = new Set<string>();
  const out: EgressRecord[] = [];
  for (const e of [...records].sort((a, b) => b.at.localeCompare(a.at))) {
    if (e.dialer !== 'box' || (e.decision !== 'deny' && e.decision !== 'held_deny')) continue;
    const key = siteOf(e.host, e.port);
    if (seen.has(key) || allow.some((a) => covers(a, e.host, e.port))) continue;
    seen.add(key);
    out.push(e);
    if (out.length >= max) break;
  }
  return out;
}

/** The shared-hosting suffix a site falls under, if any (egress.shared_tenant_suffixes). */
export function sharedSuffix(site: string, suffixes: string[]): string | undefined {
  const host = site.replace(/:\d+$/, '').replace(/^\*\./, '.');
  return suffixes.find((s) => host.endsWith(s) || `.${host}` === s);
}
