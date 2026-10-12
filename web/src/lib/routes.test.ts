import { describe, expect, it } from 'vitest';
import { match, resolve } from './routes';

describe('routes', () => {
  it('matches static and parameterised patterns', () => {
    expect(resolve('/chat')).toEqual({ pattern: '/chat', params: {} });
    expect(resolve('/chat/ses_01')).toEqual({ pattern: '/chat/:id', params: { id: 'ses_01' } });
    expect(resolve('/settings/vault/')).toEqual({ pattern: '/settings/:page', params: { page: 'vault' } });
  });

  it('rejects unknown paths and extra segments', () => {
    expect(resolve('/nope')).toBeNull();
    expect(resolve('/chat/a/b')).toBeNull();
  });

  it('decodes params and refuses malformed escapes', () => {
    expect(match('/runs/:id', '/runs/a%20b')).toEqual({ id: 'a b' });
    expect(match('/runs/:id', '/runs/%E0%A4%A')).toBeNull();
  });
});
