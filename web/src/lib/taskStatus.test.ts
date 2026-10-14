import { describe, expect, it } from 'vitest';
import { matchesResult, matchesWhere, outcome, where } from './taskStatus';

describe('task outcome words (docs/design/ui.md §10.1)', () => {
  it('says nothing extra for a finished task but its steps, and adds Refined', () => {
    expect(outcome({ status: 'done', steps: 6 })).toEqual({ text: '6 steps', tone: 'muted' });
    expect(outcome({ status: 'done', steps: 1, refined: true }).text).toBe('1 step · Refined');
  });

  it('colours only what needs attention', () => {
    expect(outcome({ status: 'waiting_approval', steps: 2 })).toEqual({ text: 'Waiting for approval', tone: 'warning' });
    expect(outcome({ status: 'parked', steps: 2, votes: { approved: 1, required: 2 } }).text).toBe('Waiting for approval (1 of 2)');
    expect(outcome({ status: 'failed', steps: 2, reason: 'the GitHub token was rejected' })).toEqual({ text: 'Failed: the GitHub token was rejected', tone: 'danger' });
    expect(outcome({ status: 'failed', steps: 2 }).text).toBe('Failed');
    expect(outcome({ status: 'budget_exceeded', steps: 9 })).toEqual({ text: 'Hit the spending limit', tone: 'warning' });
    for (const [status, text] of [
      ['stopped', 'Stopped'],
      ['interrupted', 'Interrupted'],
      ['skipped', 'Skipped'],
    ])
      expect(outcome({ status, steps: 0 })).toEqual({ text, tone: 'muted' });
  });

  it('names where a task came from', () => {
    expect(where('slack', 'message')).toBe('Slack');
    expect(where(undefined, 'message')).toBe('Web');
    expect(where('web', 'api')).toBe('API');
    expect(where('schedule', 'schedule')).toBe('Web');
  });

  it('filters by result and by place', () => {
    expect(matchesResult('parked', 'attention')).toBe(true);
    expect(matchesResult('done', 'attention')).toBe(false);
    expect(matchesResult('loop_detected', 'failed')).toBe(true);
    expect(matchesWhere('telegram', 'message', 'apps')).toBe(true);
    expect(matchesWhere('web', 'message', 'chats')).toBe(true);
    expect(matchesWhere(undefined, 'heartbeat', 'schedules')).toBe(true);
    expect(matchesWhere('web', 'api', 'chats')).toBe(false);
  });
});
