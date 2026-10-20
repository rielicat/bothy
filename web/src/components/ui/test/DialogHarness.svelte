<script lang="ts">
  import Dialog from '../Dialog.svelte';

  let {
    initialFocus = 'first',
    dirty = false,
    busy = false,
    cancel,
    onclose,
  }: { initialFocus?: 'first' | 'cancel'; dirty?: boolean; busy?: boolean; cancel?: boolean | string; onclose?: () => void } = $props();
  let open = $state(false);
</script>

<button type="button" onclick={() => (open = true)}>Open dialog</button>
<p data-testid="state">{open ? 'open' : 'closed'}</p>
<Dialog bind:open title="Add person" {initialFocus} {dirty} {busy} {cancel} {onclose}>
  <p>Fill in their details.</p>
  <label>Name <input /></label>
  {#snippet footer({ close })}
    {#if !cancel}<button type="button" data-cancel onclick={close}>Cancel</button>{/if}
    <button type="button">Add person</button>
  {/snippet}
</Dialog>
