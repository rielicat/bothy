import { describe, expect, it } from 'vitest';
import { hostPort, siteGrant } from './grants';

describe('siteGrant', () => {
  it('is a narrow domain grant the egress proxy honours', () => {
    // The proxy ignores domain grants below external_send (a box can upload).
    expect(siteGrant('usr_1', 'PyPI.org', 443)).toEqual({
      principal_id: 'usr_1',
      kind: 'domain',
      value: 'pypi.org:443',
      max_se: 'external_send',
    });
    expect(siteGrant('usr_1', '2001:db8::1', 8443).value).toBe('[2001:db8::1]:8443');
  });
});

describe('hostPort', () => {
  it('adds the default port and keeps an explicit one', () => {
    expect(hostPort('API.example.com')).toBe('api.example.com:443');
    expect(hostPort('api.example.com:8443')).toBe('api.example.com:8443');
    expect(hostPort('pypi.org', 80)).toBe('pypi.org:80');
  });

  it('brackets IPv6 literals', () => {
    expect(hostPort('2001:db8::1')).toBe('[2001:db8::1]:443');
    expect(hostPort('[2001:db8::1]')).toBe('[2001:db8::1]:443');
    expect(hostPort('[2001:db8::1]:8080')).toBe('[2001:db8::1]:8080');
  });
});
