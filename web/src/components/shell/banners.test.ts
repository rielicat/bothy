import { describe, expect, it } from 'vitest';
import type { BannerData, UsageToday } from '../../lib/api/types.gen';
import { chooseBanner, providerName, type BannerInput } from './banners';

const b = (code: string, text = ''): BannerData => ({ level: 'error', code, text });
const input = (over: Partial<BannerInput> = {}): BannerInput => ({ banners: [], paused: false, admin: true, today: null, dismissed: [], ...over });
const limit = (spent: number, cap: number): UsageToday => ({ spent_micros: 0, limit_micros: 0, global_spent_micros: spent, global_limit_micros: cap });

describe('chooseBanner (§4.9)', () => {
  it('shows nothing when nothing stops work', () => {
    expect(chooseBanner(input())).toBeNull();
    for (const code of ['no_sandbox', 'sandbox_not_isolated', 'mcp_pins', 'mcp_drifted', 'budget_80', 'unknown_sender', 'skills_changed']) expect(chooseBanner(input({ banners: [b(code)] })), code).toBeNull();
  });

  it('says each condition in its own words, never the server text', () => {
    const cases: [BannerData[] | UsageToday, string, string | undefined, boolean][] = [
      [[b('paused', 'Bothy is paused by an admin.')], 'Bothy is paused. Nothing new will start until an admin resumes it.', 'Resume', false],
      [[b('vault_sealed', 'The vault is sealed.')], "Bothy's saved keys are locked, so it can't reach its models or connections.", 'Unlock', false],
      [[b('provider_auth', 'anthropic: 401 invalid x-api-key')], "Anthropic stopped accepting Bothy's key. Chats will fail until it's replaced.", 'Update key', false],
      [[b('safe_mode', 'Started in safe mode: bothy.ui.yaml could not be loaded.')], "Bothy started with safe defaults because some settings couldn't be loaded.", "See what's wrong", true],
      [[b('config_error', 'yaml: line 3')], "Bothy started with safe defaults because some settings couldn't be loaded.", "See what's wrong", true],
      [limit(5_000_000, 5_000_000), "Bothy reached today's $5.00 spending limit. New work waits until midnight.", 'Change limit', false],
      [[b('restart_required', 'Restart to apply: sandbox.backend')], 'Restart Bothy to apply your changes.', undefined, true],
    ];
    for (const [given, text, action, dismissible] of cases) {
      const got = chooseBanner(Array.isArray(given) ? input({ banners: given }) : input({ today: given }));
      expect(got?.text).toBe(text);
      expect(got?.action?.label).toBe(action);
      expect(got?.dismissible).toBe(dismissible);
    }
  });

  it('takes tone from the table: warning for paused and the limit, danger for keys and settings, neutral for restart', () => {
    expect(chooseBanner(input({ paused: true }))?.tone).toBe('warning');
    expect(chooseBanner(input({ today: limit(6_000_000, 5_000_000) }))?.tone).toBe('warning');
    expect(chooseBanner(input({ banners: [b('vault_sealed')] }))?.tone).toBe('danger');
    expect(chooseBanner(input({ banners: [b('restart_required')] }))?.tone).toBe('neutral');
  });

  it('shows one banner, highest priority first', () => {
    const all = [b('restart_required'), b('safe_mode'), b('provider_auth', 'openai: 401'), b('vault_sealed'), b('paused')];
    expect(chooseBanner(input({ banners: all }))?.code).toBe('paused');
    expect(chooseBanner(input({ banners: all.slice(0, 4) }))?.code).toBe('vault_sealed');
    expect(chooseBanner(input({ banners: all.slice(0, 3) }))?.code).toBe('provider_auth');
    expect(chooseBanner(input({ banners: all.slice(0, 2), today: limit(1, 1) }))?.code).toBe('safe_mode');
    expect(chooseBanner(input({ banners: all.slice(0, 1), today: limit(1, 1) }))?.code).toBe('spend_limit');
  });

  it('shows members only the pause, without Resume', () => {
    const all = [b('vault_sealed'), b('provider_auth'), b('safe_mode'), b('restart_required')];
    expect(chooseBanner(input({ admin: false, banners: all, today: limit(1, 1) }))).toBeNull();
    const paused = chooseBanner(input({ admin: false, paused: true }));
    expect(paused?.code).toBe('paused');
    expect(paused?.action).toBeUndefined();
  });

  it('skips a dismissed banner for the next one, but never hides one that stops work', () => {
    expect(chooseBanner(input({ banners: [b('restart_required'), b('safe_mode')], dismissed: ['safe_mode'] }))?.code).toBe('restart_required');
    expect(chooseBanner(input({ banners: [b('restart_required')], dismissed: ['restart_required'] }))).toBeNull();
    expect(chooseBanner(input({ paused: true, dismissed: ['paused'] }))?.code).toBe('paused');
  });

  it('leaves out what another surface already says', () => {
    expect(chooseBanner(input({ banners: [b('vault_sealed'), b('restart_required')], hide: ['vault_sealed'] }))?.code).toBe('restart_required');
  });

  it('ignores a limit of zero (none set)', () => {
    expect(chooseBanner(input({ today: limit(9, 0) }))).toBeNull();
  });
});

describe('providerName', () => {
  it('reads the provider from the start of the server text', () => {
    expect(providerName('anthropic: 401 invalid x-api-key')).toBe('Anthropic');
    expect(providerName('openai: insufficient_quota')).toBe('OpenAI');
    expect(providerName('bedrock: AccessDenied')).toBe('Amazon Bedrock');
    expect(providerName('something went wrong')).toBeNull();
    expect(providerName('acme: 401')).toBeNull();
  });

  it('falls back to a sentence without a name', () => {
    expect(chooseBanner(input({ banners: [b('provider_auth', 'acme: 401')] }))?.text).toBe("The AI model service stopped accepting Bothy's key. Chats will fail until it's replaced.");
  });
});
