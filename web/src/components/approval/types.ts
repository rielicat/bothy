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
 * The inbox items that are not action approvals (S12), as the cards show
 * them; items.ts reads them from GET /api/inbox. Note suggestions are
 * approvals there, so they come with the other approvals.
 */
export type InboxItem = McpReviewItem | SkillReviewItem | SenderItem | GroupChatItem;

interface ItemBase {
  id: string;
  created_at: string;
}

export interface McpReviewItem extends ItemBase {
  type: 'mcp_review';
  server: string;
  /** `prose`: only the description changed, so `diff` compares the two sentences rather than the definitions. */
  /** `fresh`: a new action, with nothing approved before it to compare. */
  tools: { name: string; title?: string; description: string; side_effect: string; diff?: string; prose?: boolean; fresh?: boolean; hash: string; private: boolean; field_map?: Record<string, string> }[];
}

export interface SkillReviewItem extends ItemBase {
  type: 'skill_review';
  skill: string;
  hash: string;
  /** Files that differ from the approved ones, each with its diff when it is text. */
  files: { path: string; change: 'added' | 'removed' | 'changed' | string; diff?: string }[];
  /** The skill was approved before Bothy kept approved files: only that it changed is known. */
  noBaseline: boolean;
  /** More changed than the card shows; the skill's page has every file. */
  truncated: boolean;
}

export interface SenderItem extends ItemBase {
  type: 'unknown_sender';
  app: string;
  external_id: string;
  display_name: string;
  handle: string;
  text?: string;
}

export interface GroupChatItem extends ItemBase {
  type: 'group_chat';
  app: string;
  chat: string;
  space: string;
  added_by?: string;
  /** The Discord server or Slack workspace. */
  team?: string;
  /** People in the chat (Discord: in the server); 0 when unknown. */
  members: number;
  /** Slack says people from other companies are in it. */
  external: boolean;
  mention_only: boolean;
}

/** The person looking at a card. Defaults to the signed-in user. */
export interface Viewer {
  id: string;
  role: string;
  name?: string;
}
