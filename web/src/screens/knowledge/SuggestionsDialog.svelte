<script lang="ts">
  import { ApprovalCard } from '../../components/approval';
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';
  import type { Approval } from '../../lib/api/types.gen';

  // Bothy's pending note edits for one project, as the same approval cards the
  // inbox shows (D31). Deciding the last one closes the dialog.
  let { open = $bindable(false), items }: { open?: boolean; items: Approval[] } = $props();

  $effect(() => {
    if (open && !items.length) open = false;
  });
</script>

<Dialog bind:open title="Suggested changes" size="lg">
  <div class="space-y-4">
    {#each items as a (a.id)}
      <ApprovalCard approval={a} />
    {/each}
  </div>
  <!-- On phones the sheet has no close button of its own; this one sits in its header bar. -->
  {#snippet footer({ close })}<Button onclick={close}>Close</Button>{/snippet}
</Dialog>
