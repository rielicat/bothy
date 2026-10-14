<script lang="ts">
  import Dialog from '../Dialog.svelte';

  let {
    initialFocus = 'first',
    dirty = false,
    busy = false,
    onclose,
  }: { initialFocus?: 'first' | 'cancel'; dirty?: boolean; busy?: boolean; onclose?: () => void } = $props();
  let open = $state(false);
</script>

<button type="button" onclick={() => (open = true)}>Open dialog</button>
<p data-testid="state">{open ? 'open' : 'closed'}</p>
<Dialog bind:open title="Add person" {initialFocus} {dirty} {busy} {onclose}>
  <p>Fill in their details.</p>
  <label>Name <input /></label>
  {#snippet footer({ close })}
    <button type="button" data-cancel onclick={close}>Cancel</button>
    <button type="button">Add person</button>
  {/snippet}
</Dialog>
