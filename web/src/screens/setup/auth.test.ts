import { describe, expect, it } from 'vitest';
import { ApiError } from '../../lib/api/client';
import { codeError, loginError, ownerError, setupCommands, tunnel } from './auth';

describe('setup commands (S1)', () => {
  it('leads with the command for this install and lists the other two', () => {
    expect(setupCommands(undefined)).toMatchObject({ main: 'docker compose exec bothy /bothy setup-token', more: 'Not using Docker Compose?' });
    expect(setupCommands('unknown').main).toBe('docker compose exec bothy /bothy setup-token');
    expect(setupCommands('compose').rest.map((c) => c.label)).toEqual(['Docker', 'Installed directly']);
    expect(setupCommands('docker')).toMatchObject({ main: 'docker exec bothy /bothy setup-token', more: 'Not using Docker?' });
    expect(setupCommands('binary').main).toBe('bothy setup-token');
  });
});

describe('error copy', () => {
  const api = (status: number, message: string) => new ApiError(status, 'x', message);

  it('maps setup code failures', () => {
    expect(codeError(api(401, 'invalid or expired setup token'))).toBe("That code didn't work. It may have expired, so run the command below to get a new one.");
    expect(codeError(api(429, 'too many setup claims'))).toBe('Too many tries. Wait a minute, then try again.');
    expect(codeError(new TypeError('Failed to fetch'))).toBe("Bothy didn't respond. Check that it's still running, then try again.");
    expect(codeError(api(503, 'unavailable'))).toBe("Bothy didn't respond. Check that it's still running, then try again.");
  });

  it('sends owner failures to the field they belong to', () => {
    expect(ownerError(api(401, 'setup session expired: claim the setup token again'))).toEqual({ text: 'Setup timed out. Paste the setup code again.', expired: true });
    expect(ownerError(api(400, 'this password is too common or predictable; a few unrelated words work well'))).toEqual({ field: 'password', text: 'This password is too common. Try a phrase of a few words.' });
    expect(ownerError(api(400, 'password must be at least 12 characters'))).toEqual({ field: 'password', text: 'Use at least 12 characters.' });
    expect(ownerError(api(400, "username must be 1-32 letters, digits, '.', '_' or '-'")).field).toBe('username');
    expect(ownerError(new TypeError('Failed to fetch')).text).toBe("Bothy didn't respond. Check that it's still running, then try again.");
  });

  it('maps sign-in failures, with the lockout in minutes', () => {
    expect(loginError(api(401, 'invalid username or password'))).toBe("That username and password don't match.");
    expect(loginError(api(429, 'too many failed sign-in attempts: try again in 4m30s'))).toBe('Too many tries. Try again in 5 minutes.');
    expect(loginError(api(429, 'account temporarily locked: try again in 40s'))).toBe('Too many tries. Try again in 1 minute.');
    expect(loginError(api(403, 'account disabled'))).toBe('This account is turned off. Ask an admin to turn it back on.');
    expect(loginError(new TypeError('Failed to fetch'))).toBe("Bothy didn't respond. Check your connection, then try again.");
  });
});

describe('remote setup', () => {
  it("uses the server's tunnel command verbatim and the address it forwards", () => {
    expect(tunnel('ssh -L 8080:127.0.0.1:8080 <host>, then open the printed setup URL on http://127.0.0.1:8080')).toEqual({
      cmd: 'ssh -L 8080:127.0.0.1:8080 <host>',
      url: 'http://localhost:8080/setup',
    });
    expect(tunnel('ssh -L 9090:127.0.0.1:9090 ops@box')).toEqual({ cmd: 'ssh -L 9090:127.0.0.1:9090 ops@box', url: 'http://localhost:9090/setup' });
    expect(tunnel(undefined).cmd).toBe('ssh -L 8080:127.0.0.1:8080 you@your-server');
  });
});
