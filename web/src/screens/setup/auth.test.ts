import { describe, expect, it } from 'vitest';
import { ApiError } from '../../lib/api/client';
import { codeError, connectErrorText, loginError, ownerError, providerName, setupCommands, signInErrorText, takeRefused, tunnel } from './auth';

describe('the refused account', () => {
  it('is read once from the cookie the server set', () => {
    document.cookie = 'bothy_signin_who=kim%40gmail.com; Path=/';
    expect(takeRefused()).toBe('kim@gmail.com');
    expect(takeRefused()).toBe('');
    document.cookie = 'bothy_signin_who=%E0%A4%A; Path=/';
    expect(takeRefused()).toBe('');
    expect(document.cookie).not.toContain('bothy_signin_who');
  });

  it('over HTTPS comes from the __Host- cookie only', () => {
    // Anyone who can answer plain HTTP for the host can plant the plain cookie.
    document.cookie = 'bothy_signin_who=ceo%40acme.com; Path=/';
    expect(signInErrorText('not_allowed', 'google', takeRefused(true))).toBe("That Google account isn't on this Bothy's sign-in list. Ask an admin to add you.");
    document.cookie = 'bothy_signin_who=; Max-Age=0; Path=/';
  });
});

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
      placeholder: true,
    });
    expect(tunnel('ssh -L 9090:127.0.0.1:9090 ops@box')).toEqual({ cmd: 'ssh -L 9090:127.0.0.1:9090 ops@box', url: 'http://localhost:9090/setup', placeholder: false });
    expect(tunnel(undefined).cmd).toBe('ssh -L 8080:127.0.0.1:8080 you@your-server');
  });
});

describe('provider sign-in copy (§5.9, §11.2)', () => {
  it('names the provider and only a plausible account', () => {
    expect(providerName('google')).toBe('Google');
    expect(providerName('github')).toBe('GitHub');
    expect(providerName('okta')).toBe('');
    expect(signInErrorText('not_allowed', 'google', 'pat@gmail.com')).toBe("pat@gmail.com isn't on this Bothy's sign-in list. Ask an admin to add you.");
    expect(signInErrorText('not_allowed', 'github', 'octo')).toBe("@octo isn't on this Bothy's sign-in list. Ask an admin to add you.");
    expect(signInErrorText('not_allowed', 'github', 'Visit evil.example now')).toBe("That GitHub account isn't on this Bothy's sign-in list. Ask an admin to add you.");
    expect(signInErrorText('not_allowed', null, null)).toBe("That account isn't on this Bothy's sign-in list. Ask an admin to add you.");
    expect(signInErrorText('not_allowed', 'google', 'kim')).toBe("That Google account isn't on this Bothy's sign-in list. Ask an admin to add you.");
    expect(signInErrorText('not_allowed', 'github', '@@@')).toBe("That GitHub account isn't on this Bothy's sign-in list. Ask an admin to add you.");
    expect(signInErrorText('unverified', 'google', 'pat@gmail.com')).toBe("Google hasn't verified pat@gmail.com yet. Verify it in your Google account, then try again.");
    expect(signInErrorText('unverified', 'google', 'Visit evil.example')).toBe("Google hasn't verified that address yet. Verify it in your Google account, then try again.");
  });

  it('falls back to "isn\'t working" for codes it does not know', () => {
    expect(signInErrorText('something_new', 'github')).toBe("GitHub sign-in isn't working right now. Sign in with your username and password, or ask an admin.");
    expect(signInErrorText('cancelled', 'okta')).toBe('Signing in was cancelled.');
  });

  it('says why connecting did not finish', () => {
    expect(connectErrorText('taken', 'github')).toBe('That GitHub account is already connected to someone else here.');
    expect(connectErrorText('already_linked', 'google')).toBe("You've already connected a Google account. Disconnect it first.");
    expect(connectErrorText('signed_out', 'google')).toBe('You were signed out before Google answered. Sign in and try again.');
    expect(connectErrorText('not_allowed', 'google', 'sam@acme.com')).toBe(
      "sam@acme.com isn't on this Bothy's sign-in list, so it can't be connected. Admins add people in Settings → Security.",
    );
    expect(connectErrorText('not_allowed', 'github', null)).toBe(
      "That GitHub account isn't on this Bothy's sign-in list, so it can't be connected. Admins add people in Settings → Security.",
    );
    expect(connectErrorText('unverified', 'google', 'sam@acme.com')).toBe(
      "Google hasn't verified sam@acme.com yet. Verify it in your Google account, then try again.",
    );
    expect(connectErrorText('unavailable', 'github')).toBe("GitHub didn't respond. Try again, or ask an admin to check GitHub sign-in.");
  });
});
