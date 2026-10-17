// Which banner the shell shows (docs/design/ui.md §4.9): only conditions that
// stop work or need an admin, one at a time, highest priority first. The
// server's banner text is never shown; each code has its own sentence. Chat
// apps near their share (chat_budget_80) and the move off linked accounts
// (team_access) stop nothing, so Spending, People and Approvals say them.
import type { BannerData, UsageToday } from '../../lib/api/types.gen';
import { clock, date, money } from '../../lib/format';
import type { PauseInfo } from '../../lib/stores/session.svelte';

export type BannerAction = { label: string; href: string } | { label: string; run: 'resume' | 'unlock' };

export interface ShellBanner {
  code: string;
  tone: 'warning' | 'danger' | 'neutral';
  text: string;
  action?: BannerAction;
  /** Only banners whose condition doesn't stop work can be dismissed. */
  dismissible: boolean;
}

export interface BannerInput {
  banners: BannerData[];
  paused: boolean;
  admin: boolean;
  today: UsageToday | null;
  dismissed: string[];
  /** Codes another surface already says (the unlock screen says the keys are locked). */
  hide?: string[];
  /** Who paused Bothy and when, when the server says. */
  pause?: PauseInfo | null;
}

/** "Sam paused Bothy at 12:15 AM.", "… on Oct 12 at 10:42.", "Bothy was paused at …" without a who, or "Bothy is paused." when nothing is known. */
export function pausedSentence(p: PauseInfo | null | undefined, now = new Date()): string {
  const d = date(p?.at);
  let at = '';
  if (d) {
    const day = d.toDateString() === now.toDateString() ? '' : `on ${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', ...(d.getFullYear() === now.getFullYear() ? {} : { year: 'numeric' }) })} `;
    at = ` ${day}at ${clock(p?.at)}`;
  }
  if (p?.by) return `${p.by} paused Bothy${at}.`;
  return at ? `Bothy was paused${at}.` : 'Bothy is paused.';
}

const providers: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  bedrock: 'Amazon Bedrock',
  google: 'Google',
  gemini: 'Google',
  vertex: 'Google Vertex AI',
  azure: 'Azure OpenAI',
  ollama: 'Ollama',
  openrouter: 'OpenRouter',
  mistral: 'Mistral',
  groq: 'Groq',
  deepseek: 'DeepSeek',
  xai: 'xAI',
};

/** The provider a provider_auth banner is about; its text starts "name: …" until the server sends the name (S13). */
export function providerName(text: string): string | null {
  const id = /^\s*([a-z][\w-]*)\s*:/i.exec(text)?.[1]?.toLowerCase();
  return id ? (providers[id] ?? null) : null;
}

export function chooseBanner(input: BannerInput): ShellBanner | null {
  const find = (code: string) => input.banners.find((b) => b.code === code);
  const out: ShellBanner[] = [];
  if (input.paused || find('paused')) {
    out.push({
      code: 'paused',
      tone: 'warning',
      text: `${pausedSentence(input.pause)} Nothing new starts until an admin resumes it.`,
      action: input.admin ? { label: 'Resume', run: 'resume' } : undefined,
      dismissible: false,
    });
  }
  if (input.admin) {
    if (find('vault_sealed'))
      out.push({
        code: 'vault_sealed',
        tone: 'danger',
        text: "Bothy's saved keys are locked, so it can't reach its models or connections.",
        action: { label: 'Unlock', run: 'unlock' },
        dismissible: false,
      });
    const unread = find('provider_key_unread');
    if (unread) {
      const name = providerName(unread.text);
      out.push({
        code: 'provider_key_unread',
        tone: 'danger',
        text: name ? `Bothy couldn't read its key for ${name}. Chats will fail until it can.` : "Bothy couldn't read its AI model's key. Chats will fail until it can.",
        action: { label: "See what's wrong", href: '/settings/models' },
        dismissible: false,
      });
    }
    const auth = find('provider_auth');
    if (auth) {
      const name = providerName(auth.text);
      out.push({
        code: 'provider_auth',
        tone: 'danger',
        text: name ? `${name} stopped accepting Bothy's key. Chats will fail until it's replaced.` : "The AI model service stopped accepting Bothy's key. Chats will fail until it's replaced.",
        action: { label: 'Update key', href: '/settings/models' },
        dismissible: false,
      });
    }
    if (find('safe_mode') || find('config_error'))
      out.push({
        code: 'safe_mode',
        tone: 'danger',
        text: "Bothy started with safe defaults because some settings couldn't be loaded.",
        action: { label: "See what's wrong", href: '/settings/advanced' },
        dismissible: true,
      });
    const t = input.today;
    if (t && t.global_limit_micros > 0 && t.global_spent_micros >= t.global_limit_micros)
      out.push({
        code: 'spend_limit',
        tone: 'warning',
        text: `Bothy reached today's ${money(t.global_limit_micros)} spending limit. New work waits until midnight.`,
        action: { label: 'Change limit', href: '/settings/general' },
        dismissible: false,
      });
    if (find('restart_required'))
      out.push({ code: 'restart_required', tone: 'neutral', text: 'Restart Bothy to apply your changes.', action: { label: 'Open Advanced', href: '/settings/advanced' }, dismissible: true });
  }
  return out.find((b) => !input.hide?.includes(b.code) && !(b.dismissible && input.dismissed.includes(b.code))) ?? null;
}
