<script lang="ts">
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';

  let { open = $bindable(false), ondiscard }: { open?: boolean; ondiscard: () => void } = $props();
</script>

<Dialog bind:open title="Discard your changes?" initialFocus="cancel">
  <p class="text-body text-text">Your edits won't be saved.</p>
  {#snippet footer({ close })}
    <Button onclick={close}>Keep editing</Button>
    <Button
      variant="danger"
      onclick={() => {
        open = false;
        ondiscard();
      }}>Discard</Button
    >
  {/snippet}
</Dialog>
