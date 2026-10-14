import { describe, expect, it } from 'vitest';
import type { Action } from './api/types.gen';
import { commandOf, describeGrant, hostPort, siteGrant } from './grants';

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

describe('describeGrant', () => {
  const a = (tool: string, kind: string, payload: Record<string, unknown>, extra: Partial<Action> = {}): Action => ({ tool, kind, side_effect: 'local_write', payload, fingerprint: 'f', ...extra });

  it('names the narrow permission the gateway forms', () => {
    expect(describeGrant(a('fs_write', 'fs.write', { path: 'notes/q3/plan.md' }))).toBe('changes to files in notes/q3/');
    expect(describeGrant(a('fs_write', 'fs.write', { path: 'todo.md' }))).toBe('changes to todo.md');
    expect(describeGrant(a('fs_delete', 'fs.delete', { path: 'tmp/x' }))).toBe('deleting files in tmp/');
    expect(describeGrant(a('shell', 'exec', { argv: ['bash', '-lc', 'npm test'] }))).toBe('running npm test');
    expect(describeGrant(a('shell', 'exec', { argv: ['make', 'test'] }))).toBe('running make commands');
    expect(describeGrant(a('python', 'exec', { argv: ['python3', 'x.py'], code: 'print(1)' }))).toBe('running this exact script');
    expect(describeGrant(a('web_fetch', 'http', {}, { host: 'api.example.com', port: 443 }))).toBe('contacting api.example.com');
    expect(describeGrant(a('net_allow', 'net', {}, { host: 'db.internal', port: 5432 }))).toBe('contacting db.internal:5432');
    expect(describeGrant(a('message', 'send', {}, { recipient: 'slack:C1' }))).toBe('sending messages to this Slack chat');
    expect(describeGrant(a('skill', 'generic', { name: 'x' }))).toBe('this exact request');
    expect(describeGrant(undefined)).toBe('this exact request');
  });

  it('reads the command of a bash -lc call', () => {
    expect(commandOf({ argv: ['bash', '-lc', 'ls -la'], payload: {} })).toBe('ls -la');
    expect(commandOf({ payload: { argv: ['make', 'build'] } })).toBe('make build');
  });
});
