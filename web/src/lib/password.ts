import { plural } from './format';

// The server enforces the minimum and the common-password list (SPEC §9.1
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

/** @deprecated v0.1 strength meter; only format.test.ts still uses it, and C10 removes both. */
export function strength(pw: string): { score: number; label: string } {
  if (!pw) return { score: 0, label: '' };
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length;
  const unique = new Set(pw).size;
  let score = pw.length >= 12 ? 1 : 0;
  if (pw.length >= 14) score++;
  if (pw.length >= 18 || (pw.length >= 12 && classes >= 3)) score++;
  if (pw.length >= 20 && classes >= 2 && unique >= 10) score++;
  if (unique < 5) score = Math.min(score, 1);
  const labels = ['Too short', 'Okay', 'Good', 'Strong', 'Excellent'];
  return { score, label: labels[score] };
}
