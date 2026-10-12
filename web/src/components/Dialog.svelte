<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  let {
    open = $bindable(false),
    title,
    kind = 'modal',
    children,
    footer,
    onclose,
  }: { open?: boolean; title: string; kind?: 'modal' | 'sheet'; children: Snippet; footer?: Snippet; onclose?: () => void } = $props();

  const id = $props.id();
  let el: HTMLDialogElement;

  // showModal gives focus trapping, Esc and inertness of the page for free.
  $effect(() => {
    if (open && !el.open) el.showModal();
    else if (!open && el.open) el.close();
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<dialog
  bind:this={el}
  class="{kind === 'sheet' ? 'sheet' : 'modal'} flex-col open:flex"
  aria-labelledby="{id}-t"
  onclose={() => {
    open = false;
    onclose?.();
  }}
  onclick={(e) => e.target === el && el.close()}
>
  <div class="flex items-center gap-2 border-b border-line px-4 py-3">
    <h2 id="{id}-t" class="flex-1 font-semibold">{title}</h2>
    <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Close" onclick={() => el.close()}>
      <Icon name="x" size={16} />
    </button>
  </div>
  <div class="min-h-0 flex-1 overflow-y-auto p-4">{@render children()}</div>
  {#if footer}
    <div class="flex flex-wrap justify-end gap-2 border-t border-line px-4 py-3">{@render footer()}</div>
  {/if}
</dialog>
