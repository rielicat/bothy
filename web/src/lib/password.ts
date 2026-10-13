// Local strength hint only; the server enforces the length minimum and the
// common-password list (SPEC §9.1 keeps that list out of the bundle).
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
