<script lang="ts">
  import { ApprovalCard } from '../../components/approval';
  import Dialog from '../../components/ui/Dialog.svelte';
  import type { Approval } from '../../lib/api/types.gen';

  // Bothy's pending note edits for one project, as the same approval cards the
  // inbox shows (D31). Deciding the last one closes the dialog.
  let { open = $bindable(false), items }: { open?: boolean; items: Approval[] } = $props();

  $effect(() => {
    if (open && !items.length) open = false;
  });
</script>

<!-- On phones the sheet has no close button of its own; Close sits in its header bar. -->
<Dialog bind:open title="Suggested changes" size="lg" cancel="Close">
  <div class="space-y-4">
    <!-- The cards look alike until opened, so each shows its change straight away. -->
    {#each items as a (a.id)}
      <ApprovalCard approval={a} showOpen />
    {/each}
  </div>
</Dialog>
