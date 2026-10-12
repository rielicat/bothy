import { describe, expect, it, vi } from 'vitest';
import { api, ApiError, onUnauthorized } from './client';

describe('api client', () => {
  it('treats a 401 as a dead session except where it judges a typed credential', async () => {
    vi.stubGlobal('fetch', async () => new Response('{"error":{"code":"unauthorized","message":"nope"}}', { status: 401 }));
    const expired = vi.fn();
    onUnauthorized(expired);
    await expect(api.post('/api/auth/password', { old: 'x', new: 'y' })).rejects.toBeInstanceOf(ApiError);
    await expect(api.post('/api/auth/login', {})).rejects.toBeInstanceOf(ApiError);
    expect(expired).not.toHaveBeenCalled();
    await expect(api.get('/api/sessions')).rejects.toMatchObject({ status: 401, code: 'unauthorized' });
    expect(expired).toHaveBeenCalledTimes(1);
  });
});
