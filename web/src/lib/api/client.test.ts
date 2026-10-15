import { describe, expect, it, vi } from 'vitest';
import { api, ApiError, errorText, onUnauthorized } from './client';

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

  it('keeps a command fix in the error text and leaves a route out of it', () => {
    expect(errorText(new ApiError(503, 'sealed', "Bothy's saved keys are locked.", '/settings/security/keys'))).toBe("Bothy's saved keys are locked.");
    expect(errorText(new ApiError(503, 'unavailable', 'safe mode: fix bothy.yaml', 'bothy config check'))).toBe('safe mode: fix bothy.yaml bothy config check');
  });
});
