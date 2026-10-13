import type { GrantCreateReq, PrincipalID } from './api/types.gen';

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
