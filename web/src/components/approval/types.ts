// Shapes the approval UI reads that types.gen.ts does not have (docs/design/ui.md §17).
import type { ChatRef } from '../../lib/api/types.gen';

/** S17: one deploy as GET /api/releases lists it. */
export interface Deploy {
  id: string;
  environment: string;
  repo: string;
  ref: string;
  approval_id?: string;
  status: 'dispatched' | 'running' | 'succeeded' | 'failed' | 'unknown' | string;
  run_url?: string;
  error?: string;
  /** The workflow step that failed, when the server knows it (S17). */
  failed_step?: string;
  created_at: string;
  finished_at?: string;
  chat?: ChatRef;
}

/**
 * S12: the inbox items that are not action approvals. SV1 has not fixed the
 * endpoint yet; the client reads GET /api/approvals/items and decides with
 * POST /api/approvals/items/{id} {choice}. Without the endpoint the inbox
 * shows action approvals only and these stay on their settings pages.
 */
export type InboxItem = McpReviewItem | SkillReviewItem | SenderItem | GroupChatItem | NoteSuggestionItem;

interface ItemBase {
  id: string;
  created_at: string;
}

export interface McpReviewItem extends ItemBase {
  type: 'mcp_review';
  server: string;
  tools: { name: string; title?: string; description: string; side_effect: string; diff?: string }[];
}

export interface SkillReviewItem extends ItemBase {
  type: 'skill_review';
  skill: string;
  diff: string;
}

export interface SenderItem extends ItemBase {
  type: 'unknown_sender';
  app: string;
  display_name: string;
  handle: string;
  text?: string;
}

export interface GroupChatItem extends ItemBase {
  type: 'group_chat';
  app: string;
  chat: string;
  added_by?: string;
}

export interface NoteSuggestionItem extends ItemBase {
  type: 'note_suggestion';
  repo: string;
  diff: string;
}

/** The person looking at a card. Defaults to the signed-in user. */
export interface Viewer {
  id: string;
  role: string;
  name?: string;
}
