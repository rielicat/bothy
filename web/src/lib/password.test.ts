import { describe, expect, it } from 'vitest';
import { nameError, passwordError, passwordHelp, usernameError } from './password';

describe('new password help (§5.5)', () => {
  it('counts what is still needed, in code points', () => {
    expect(passwordHelp('')).toBe('At least 12 characters.');
    expect(passwordHelp('a')).toBe('11 more characters needed.');
    expect(passwordHelp('abcdefghijk')).toBe('1 more character needed.');
    expect(passwordHelp('abcdefghijkl')).toBe('Long enough.');
    // Emoji are one character each, as the server counts runes.
    expect(passwordHelp('🔑🔑🔑🔑🔑🔑🔑🔑🔑🔑🔑')).toBe('1 more character needed.');
  });

  it('validates the account fields with the plan copy', () => {
    expect(nameError(' ')).toBe('Enter your name.');
    expect(nameError('Maya Chen')).toBe('');
    expect(nameError('x'.repeat(65))).toBe('Enter your name.');
    expect(usernameError('maya.chen_2')).toBe('');
    expect(usernameError('-maya')).toMatch(/^Use up to 32 letters/);
    expect(usernameError('a'.repeat(33))).toMatch(/^Use up to 32 letters/);
    expect(usernameError('maya chen')).toMatch(/^Use up to 32 letters/);
    expect(passwordError('short')).toBe('Use at least 12 characters.');
    expect(passwordError('long enough pass')).toBe('');
  });
});
