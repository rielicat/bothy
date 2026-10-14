import type { Choice } from './api/types.gen';

export const choiceDone: Record<Choice, string> = {
  allow_once: 'Approved',
  allow_session: 'Allowed for this chat',
  allow_always: 'Always allowed',
  deny: 'Denied',
};

// "Always allow" creates a standing grant, so it has no single-key shortcut
// that a stray keypress could trigger; it stays a button.
export const choiceKey: Partial<Record<Choice, string>> = { allow_once: 'a', allow_session: 's', deny: 'd' };

export const channelName: Record<string, string> = {
  web: 'Web',
  telegram: 'Telegram',
  discord: 'Discord',
  slack: 'Slack',
  api: 'API',
  schedule: 'Schedule',
};
