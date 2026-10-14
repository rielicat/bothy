// The plain-copy rules of docs/design/ui.md §18.2 G6 and §14, as data. Pure,
// so both the browser assertion (copy.ts) and C10's source scan
// (src/copy.test.ts) can share them.

export interface CopyRule {
  id: string;
  pattern: RegExp;
  why: string;
}

/** G6 pattern 1: words kept out of the main flow (§14.3). */
export const BANNED_WORDS =
  /\b(context window|TTFT|cron|sandbox|egress|principal|use_tool|tool_search|trifecta|micro-?dollars?|heartbeat|session|vault|SOUL\.md|USER\.md|MEMORY\.md|HEARTBEAT\.md|household)\b/i;

/** G6 pattern 2: model usage in tokens instead of money. */
export const TOKEN_TALK = /\btokens?\s+(in|out|used|of)\b|\b\d[\d.,]*K?\s+tokens\b/i;

export const COPY_RULES: CopyRule[] = [
  { id: 'banned-word', pattern: BANNED_WORDS, why: 'a word kept out of the main flow (§14.3)' },
  { id: 'token-talk', pattern: TOKEN_TALK, why: 'usage in tokens; show money (§14.3)' },
  { id: 'exclamation', pattern: /!/, why: 'no exclamation marks (§14.2)' },
  { id: 'id', pattern: /\b(usr|ses|run|apr|sch)_[0-9a-z]+/i, why: 'an id outside Details (§14.2)' },
  { id: 'untitled-chat', pattern: /\buntitled chat\b/i, why: 'chats are titled by their first words (§4.2)' },
  // "never" as a timestamp ("Last used: never", "· never"), not as a word in a sentence.
  { id: 'never-as-time', pattern: /^never$|\b(?:last (?:used|run|seen|active)|updated|used|ran)\s*:?\s*never\b|·\s*never\b/i, why: '"never" as a time (§4.2)' },
];

export interface CopyMatch {
  rule: string;
  match: string;
}

/** Every rule the text breaks, with the matched characters. */
export function copyMatches(text: string, rules: CopyRule[] = COPY_RULES): CopyMatch[] {
  const out: CopyMatch[] = [];
  const t = text.replace(/\s+/g, ' ').trim();
  for (const r of rules) {
    const m = r.pattern.exec(t);
    if (m) out.push({ rule: r.id, match: m[0] });
  }
  return out;
}
