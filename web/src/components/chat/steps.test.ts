import { describe, expect, it } from 'vitest';
import { refusalReason } from './steps';

describe('refusalReason', () => {
  it("turns the server's refusal into a sentence", () => {
    expect(refusalReason('Denied by policy (floor:release): a deploy to production needs 2 approvers other than you, and only 1 other person can approve it')).toBe(
      'A deploy to production needs 2 approvers other than you, and only 1 other person can approve it.',
    );
    expect(refusalReason('Denied by policy (rule:no-pipe): pipes a download into a shell.')).toBe('Pipes a download into a shell.');
  });

  it('says nothing for other results', () => {
    expect(refusalReason('Checked out acme/web')).toBe('');
    expect(refusalReason(undefined)).toBe('');
  });
});
