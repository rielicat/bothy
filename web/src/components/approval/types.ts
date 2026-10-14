// Server shapes the approval UI reads before they are in types.gen.ts
// (docs/design/ui.md §17). C10: replace each with the generated type once its
// track regenerates the file; every field is optional here so today's server
// keeps working through the fallbacks.
import type { Approval, ApproverReq, Card, ChatRef, Choice, Role, UserID } from '../../lib/api/types.gen';

/** S2: plain card copy rendered by the policy module. Absent fields fall back to lib/cardcopy.ts. */
export interface CardV2 extends Card {
  question?: string;
  consequence?: string;
  primary_label?: string;
  ing?: string;
  kind?: 'action' | 'pull_request' | 'deploy' | 'review';
  tone?: 'default' | 'destructive';
}

/** S4, as SV1 built it: who may vote on a multi-approver approval. */
export interface ApproverReqV2 extends ApproverReq {
  users?: UserID[];
  roles?: Role[];
  required?: number;
  requester?: UserID;
}

export interface ApprovalVote {
  approval_id: string;
  user_id: UserID;
  choice: Choice;
  via: string;
  at: string;
  comment?: string;
}

export interface ApprovalV2 extends Approval {
  card: CardV2;
  approver: ApproverReqV2;
  votes?: ApprovalVote[];
  /** Resolution.Comment: the reason given with a deny (S4/S8). */
  comment?: string;
  /** S8: the decider's display name. */
  decided_by_name?: string;
}

/** S4: GET /api/approvals/{id}/votes, the vote state as the caller sees it. */
export interface ApprovalVotes {
  approval_id: string;
  status: string;
  required: number;
  approvals: number;
  votes: { user_id: UserID; name: string; choice: Choice; via: string; at: string; comment?: string }[];
  /** "Maya Chen, Priya Rao or any admin" */
  approvers: string;
  requester: UserID;
  requester_name: string;
  can_vote: boolean;
  can_withdraw: boolean;
  your_vote?: Choice;
}

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
