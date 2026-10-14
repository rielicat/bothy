<script lang="ts">
  import { timeOfDay } from '../../lib/cardcopy';
  import { fullTime, plural } from '../../lib/format';
  import Avatar from '../ui/Avatar.svelte';
  import type { ApprovalVotes } from './types';

  // The Approvals row of a multi-approver card (§8.5): "1 of 2", each vote
  // with a name and time, and who can still approve.
  let { view, open }: { view: ApprovalVotes; open: boolean } = $props();

  const left = $derived(Math.max(0, view.required - view.approvals));
</script>

<div class="space-y-1.5">
  <p class="tabular-nums">{view.approvals} of {view.required}</p>
  {#each view.votes as v (v.user_id)}
    <p class="flex items-center gap-2">
      <Avatar name={v.name || '?'} />
      <span>{v.name || 'Someone'} {v.choice === 'deny' ? 'denied' : 'approved'} · <time datetime={v.at} title={fullTime(v.at)}>{timeOfDay(v.at)}</time></span>
    </p>
  {/each}
  {#if open && left > 0 && view.approvers}
    <p class="text-small text-muted">{left === 1 ? 'One more' : plural(left, 'more', 'more')} from {view.approvers}</p>
  {/if}
</div>
