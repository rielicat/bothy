// The approval components (docs/design/ui.md §15.3 row 42).
//
// <ApprovalCard approval mode viewer? onresolve? />
//   mode 'inline' (chat: collapses to its DecidedLine once decided, A/S/D while
//   focused), 'inbox' (expanded inbox row), 'decided' (the line with "Show"),
//   'readonly' (no buttons). The card sends the decision itself and then calls
//   onresolve(choice, comment, updatedApproval).
// <DecidedLine approval /> the one-line record of a decided approval.
// <DeployFollowUp approval /> what became of an approved deploy (§8.7).
export { default as ApprovalCard } from './ApprovalCard.svelte';
export { default as ApprovalFields } from './ApprovalFields.svelte';
export { default as ApprovalVotes } from './ApprovalVotes.svelte';
export { default as AlwaysAllowDialog } from './AlwaysAllowDialog.svelte';
export { default as DecidedLine } from './DecidedLine.svelte';
export { default as DeployFollowUp } from './DeployFollowUp.svelte';
export { default as ReviewCard } from './ReviewCard.svelte';
export type { ApprovalV2, ApprovalVotes as ApprovalVotesView, InboxItem, Viewer } from './types';
