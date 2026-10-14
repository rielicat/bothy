import { describe, expect, it } from 'vitest';
import { member, ZERO } from '../../../mock/fixtures';
import type { TokenRecord } from '../../lib/api/types.gen';
import { lastUsed, moneyText, parseMoney, roleLine, scopeSummary } from './accountHelpers';

describe('API tokens', () => {
  const token = (over: Partial<TokenRecord>): TokenRecord => ({
    id: 'tok_x',
    user_id: 'usr_x',
    name: 'x',
    scopes: ['chat'],
    on_approval: 'deny',
    rate_per_min: 30,
    created_at: ZERO,
    last_used_at: ZERO,
    expires_at: ZERO,
    revoked_at: ZERO,
    ...over,
  });

  it('describes scopes in plain words', () => {
    expect(scopeSummary(['chat'])).toBe('Can chat');
    expect(scopeSummary(['read', 'chat'])).toBe('Can chat and read');
    expect(scopeSummary(['chat', 'read', 'llm'])).toBe('Can chat, read and use the AI models');
  });

  it('never says "never" for a token nobody has used', () => {
    const now = Date.parse('2026-10-14T12:00:00Z');
    expect(lastUsed(token({}), now)).toBe('Not used yet');
    expect(lastUsed(token({ last_used_at: '2026-10-14T09:00:00Z' }), now)).toBe('Last used 3 h ago');
  });
});

describe('people', () => {
  it('says someone has not signed in only when the server reports sign-ins', () => {
    expect(roleLine(member)).toBe('Member');
    expect(roleLine({ ...member, last_login_at: ZERO })).toBe("Member · hasn't signed in yet");
    expect(roleLine({ ...member, last_login_at: '2026-10-01T09:00:00Z' })).toBe('Member');
    expect(roleLine({ ...member, role: 'admin', disabled: true, last_login_at: ZERO })).toBe('Admin · Turned off');
  });
});

it('reads and writes dollar amounts with two decimals', () => {
  expect(moneyText(5)).toBe('5.00');
  expect(moneyText(undefined)).toBe('');
  expect(parseMoney('$1,200.5')).toBe(1200.5);
  expect(parseMoney('7')).toBe(7);
  expect(parseMoney('.5')).toBe(0.5);
  expect(parseMoney('-1')).toBeNull();
  expect(parseMoney('5.123')).toBeNull();
  expect(parseMoney('five')).toBeNull();
});
