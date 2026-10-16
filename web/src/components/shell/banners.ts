// Which banner the shell shows (docs/design/ui.md §4.9): only conditions that
// stop work or need an admin, one at a time, highest priority first. The
// server's banner text is never shown; each code has its own sentence.
import type { BannerData, UsageToday } from '../../lib/api/types.gen';
import { clock, date, fullTime, money } from '../../lib/format';
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

/** "since 10:42 by Maya Chen", "since 12 October 2026, 10:42", "by Maya Chen", or "" when nothing is known. */
export function pausedSince(p: PauseInfo | null | undefined, now = new Date()): string {
  const d = date(p?.at);
  const parts: string[] = [];
  if (d) parts.push(`since ${d.toDateString() === now.toDateString() ? clock(p?.at) : fullTime(p?.at)}`);
  if (p?.by) parts.push(`by ${p.by}`);
  return parts.join(' ');
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
    const since = pausedSince(input.pause);
    out.push({
      code: 'paused',
      tone: 'warning',
      text: `${since ? `Bothy has been paused ${since}.` : 'Bothy is paused.'} Nothing new will start until an admin resumes it.`,
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
    if (find('chat_budget_80'))
      out.push({
        code: 'chat_budget_80',
        tone: 'warning',
        text: "Chat apps have used 80% of their share of today's spending limit.",
        action: { label: 'See spending', href: '/activity/spending' },
        dismissible: true,
      });
    if (find('restart_required')) out.push({ code: 'restart_required', tone: 'neutral', text: 'Restart Bothy to apply your changes.', dismissible: true });
    if (find('team_access'))
      out.push({
        code: 'team_access',
        tone: 'neutral',
        text: 'People in chat apps no longer link to Bothy accounts. Check who can use Bothy and who can approve in People.',
        action: { label: 'Open People', href: '/settings/people' },
        dismissible: true,
      });
  }
  return out.find((b) => !input.hide?.includes(b.code) && !(b.dismissible && input.dismissed.includes(b.code))) ?? null;
}
