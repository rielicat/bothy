<script lang="ts">
  import { timeOfDay } from '../../lib/cardcopy';
  import { fullTime, plural } from '../../lib/format';
  import Avatar from '../ui/Avatar.svelte';
  import type { ApprovalVotes } from '../../lib/api/types.gen';

  // The Approvals row of a multi-approver card (§8.5): "1 of 2", each vote
  // with a name and time, and who can still approve. `me` reads as "You".
  // `remaining`: the viewer decides or asked; anyone else gets one sentence
  // under the card instead of the "more from" line.
  let { view, open, me, remaining = true }: { view: ApprovalVotes; open: boolean; me?: string; remaining?: boolean } = $props();

  const left = $derived(Math.max(0, view.required - view.approvals));
</script>

<div class="space-y-1.5">
  <p class="tabular-nums">{view.approvals} of {view.required}</p>
  {#each view.votes as v (v.user_id)}
    {@const mine = !!me && v.user_id === me}
    <p class="flex items-center gap-2">
      <Avatar name={v.name || '?'} />
      <span>{mine ? 'You' : v.name || 'Someone'} {v.choice === 'deny' ? 'denied' : 'approved'} · <time datetime={v.at} title={fullTime(v.at)}>{timeOfDay(v.at)}</time></span>
    </p>
  {/each}
  {#if open && remaining && left > 0 && view.approvers}
    <p class="text-small text-muted">{left === 1 ? 'One more' : plural(left, 'more', 'more')} from {view.approvers}</p>
  {/if}
</div>
