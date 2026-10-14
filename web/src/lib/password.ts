import { plural } from './format';

// The server enforces the minimum and the common-password list (SPEC §9.4
// keeps that list out of the bundle); these only count and match.
export const MIN_PASSWORD = 12;
const USERNAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/;

// Counted in code points, as the server counts runes.
const length = (s: string) => [...s].length;

/** The live help under a new password (docs/design/ui.md §5.5). */
export function passwordHelp(pw: string): string {
  const n = length(pw);
  if (!n) return `At least ${MIN_PASSWORD} characters.`;
  if (n < MIN_PASSWORD) return `${plural(MIN_PASSWORD - n, 'more character', 'more characters')} needed.`;
  return 'Long enough.';
}

export function nameError(v: string): string {
  const n = length(v.trim());
  return n >= 1 && n <= 64 ? '' : 'Enter your name.';
}

export function usernameError(v: string): string {
  return USERNAME.test(v.trim()) ? '' : 'Use up to 32 letters, numbers, dots, dashes or underscores, starting with a letter or number.';
}

export function passwordError(v: string): string {
  return length(v) >= MIN_PASSWORD ? '' : `Use at least ${MIN_PASSWORD} characters.`;
}
