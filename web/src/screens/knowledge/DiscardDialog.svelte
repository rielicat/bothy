<script lang="ts">
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';

  let { open = $bindable(false), ondiscard }: { open?: boolean; ondiscard: () => void } = $props();
</script>

<Dialog bind:open title="Discard your changes?" initialFocus="cancel" cancel="Keep editing">
  <p class="text-body text-text">This can't be undone.</p>
  {#snippet footer()}
    <Button
      variant="danger"
      onclick={() => {
        open = false;
        ondiscard();
      }}>Discard changes</Button
    >
  {/snippet}
</Dialog>
