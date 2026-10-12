import { describe, expect, it } from 'vitest';
import { hostPort } from './grants';

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
