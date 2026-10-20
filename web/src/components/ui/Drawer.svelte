<script lang="ts">
  import type { Snippet } from 'svelte';
  import IconButton from './IconButton.svelte';
  import { focusables, trapTab } from './layer';

  let {
    open = $bindable(false),
    title,
    side = 'right',
    width = 400,
    onclose,
    children,
  }: {
    open?: boolean;
    title: string;
    side?: 'right' | 'left';
    /** Pixels on wide screens; full screen under 640 px. */
    width?: number;
    onclose?: () => void;
    children: Snippet;
  } = $props();

  const uid = $props.id();
  let el: HTMLDialogElement | undefined = $state();
  let opener: HTMLElement | null = null;

  $effect(() => {
    const d = el;
    if (!d) return;
    if (open && !d.open) {
      opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      d.showModal();
      const body = d.querySelector('[data-drawer-body]');
      ((body && focusables(body)[0]) || focusables(d)[0])?.focus();
    } else if (!open && d.open) d.close();
  });

  function closed() {
    open = false;
    if (opener?.isConnected) opener.focus();
    opener = null;
    onclose?.();
  }
</script>

<!-- A modal dialog pinned to one edge: the page behind is inert and the scrim closes it. -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_click_events_have_key_events -->
<dialog
  bind:this={el}
  aria-labelledby="{uid}-title"
  aria-modal="true"
  class="fixed inset-y-0 m-0 h-dvh max-h-none max-w-full flex-col border-border bg-surface p-0 text-text shadow-overlay transition-[translate] duration-200 ease-out open:flex max-sm:w-full! {side ===
  'right'
    ? 'right-0 left-auto border-l starting:open:translate-x-full sm:rounded-l-lg'
    : 'right-auto left-0 border-r starting:open:-translate-x-full sm:rounded-r-lg'}"
  style:width="{width}px"
  oncancel={(e) => {
    e.preventDefault();
    open = false;
  }}
  onclose={closed}
  onkeydown={(e) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      open = false;
    } else if (el) trapTab(e, el);
  }}
  onclick={(e) => {
    if (e.target === el) open = false;
  }}
>
  <div class="flex h-13 shrink-0 items-center gap-3 border-b border-border pr-3 pl-5">
    <h2 id="{uid}-title" class="min-w-0 flex-1 truncate text-heading text-text">{title}</h2>
    <IconButton icon="close" label="Close" variant="quiet" onclick={() => (open = false)} />
  </div>
  {#if open}
    <div data-drawer-body class="min-h-0 flex-1 overflow-y-auto px-5 py-4">{@render children()}</div>
  {/if}
</dialog>
