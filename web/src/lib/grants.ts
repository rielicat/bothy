// core.GrantDomain values are exact "host:port" (IPv6 in brackets); a bare
// host would never match a connection, so the port is always spelled out.
export function hostPort(value: string, port = 443): string {
  const v = value.trim().toLowerCase().replace(/\.$/, '');
  if (/^\[[^\]]+\]:\d+$/.test(v) || /^[^:[\]]+:\d+$/.test(v)) return v;
  if (/^\[[^\]]+\]$/.test(v)) return `${v}:${port}`;
  if (v.includes(':')) return `[${v}]:${port}`;
  return `${v}:${port}`;
}
