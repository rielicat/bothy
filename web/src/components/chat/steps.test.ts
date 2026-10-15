import { describe, expect, it } from 'vitest';
import { refusalReason } from './steps';

describe('refusalReason', () => {
  it('keeps the deploy refusals that already read as sentences', () => {
    expect(refusalReason('Denied by policy (floor:release): a deploy to production needs 2 approvers other than you, and only 1 other person can approve it')).toBe(
      'A deploy to production needs 2 approvers other than you, and only 1 other person can approve it.',
    );
    expect(refusalReason('Denied by policy (floor:release): the staging environment has no approvers')).toBe('The staging environment has no approvers.');
    // The server's plain reasons, and an admin's own rule reason, are shown as written.
    expect(refusalReason('Denied by policy (rule:no-pipe): pipes a download into a shell.')).toBe('Pipes a download into a shell.');
    expect(refusalReason("Denied by policy (floor:release): there's no environment called “dev”")).toBe("There's no environment called “dev”.");
    expect(refusalReason("Denied by policy (floor:unavailable): Bothy can't create the schedule “Daily” during a check-in")).toBe(
      "Bothy can't create the schedule “Daily” during a check-in.",
    );
  });

  it('puts reasons stored in the old wording in plain words, without rules, tools or errors', () => {
    expect(refusalReason('Denied by policy (floor:paused): Bothy is paused by an admin; only read-only tools run')).toBe('Bothy is paused.');
    expect(refusalReason('Denied by policy (floor:release): deploys are not allowed in scheduled, heartbeat or API runs')).toBe('Deploys can only be asked for by a person in a chat.');
    expect(refusalReason('Denied by policy (floor:release): this deploy does not match the production environment: ref "x" is not allowed (main)')).toBe(
      "This deploy doesn't match how production is set up.",
    );
    for (const r of [
      'Denied by policy (default:local_write): local_write actions are denied by the policy defaults',
      'Denied by policy (floor:unavailable): shell is not available in schedule runs',
      'Denied by policy (floor:invalid): the tool call could not be checked: unexpected EOF',
    ]) {
      expect(refusalReason(r)).toBe("Bothy's rules don't allow this.");
    }
  });

  it('says nothing for other results', () => {
    expect(refusalReason('Checked out acme/web')).toBe('');
    expect(refusalReason(undefined)).toBe('');
  });
});
