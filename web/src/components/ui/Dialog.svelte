<script lang="ts">
  import type { Snippet } from 'svelte';
  import IconButton from './IconButton.svelte';
  import { focusables, trapTab } from './layer';

  let {
    open = $bindable(false),
    title,
    size = 'sm',
    initialFocus = 'first',
    dirty = false,
    busy = false,
    onclose,
    children,
    footer,
  }: {
    open?: boolean;
    title: string;
    /** 440 (confirm), 560 (form), 720 (review). On phones md and lg become full-screen sheets. */
    size?: 'sm' | 'md' | 'lg';
    /** 'cancel' for destructive confirmations: focus starts on Cancel, the first footer button. */
    initialFocus?: 'first' | 'cancel';
    /** Unsaved input: a click on the backdrop no longer closes. */
    dirty?: boolean;
    /** A request is in flight: nothing closes the dialog. */
    busy?: boolean;
    onclose?: () => void;
    children: Snippet;
    /** Cancel first, then the primary action; the footer right-aligns them. */
    footer?: Snippet<[{ close: () => void }]>;
  } = $props();

  const uid = $props.id();
  let el: HTMLDialogElement | undefined = $state();
  let opener: HTMLElement | null = null;

  const width = { sm: 'max-w-[27.5rem]', md: 'max-w-[35rem]', lg: 'max-w-[45rem]' };
  const sheet = $derived(size !== 'sm');

  function requestClose() {
    if (busy) return;
    open = false;
  }

  function focusInitial(d: HTMLDialogElement) {
    const body = d.querySelector('[data-dialog-body]');
    const foot = d.querySelector('[data-dialog-footer]');
    let target: HTMLElement | null | undefined;
    if (initialFocus === 'cancel' && foot) target = foot.querySelector<HTMLElement>('[data-cancel]') ?? focusables(foot)[0];
    if (!target && body) target = body.querySelector<HTMLElement>('input:not([type=hidden]):not([disabled]), select:not([disabled]), textarea:not([disabled])') ?? focusables(body)[0];
    if (!target && foot) target = focusables(foot)[0];
    (target ?? focusables(d)[0])?.focus();
  }

  $effect(() => {
    const d = el;
    if (!d) return;
    if (open && !d.open) {
      opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      d.showModal();
      focusInitial(d);
    } else if (!open && d.open) d.close();
  });

  function closed() {
    open = false;
    if (opener?.isConnected) opener.focus();
    opener = null;
    onclose?.();
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      // Handled here rather than by the native cancel so busy dialogs stay open everywhere.
      e.preventDefault();
      e.stopPropagation();
      requestClose();
    } else if (el) trapTab(e, el);
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_click_events_have_key_events -->
<dialog
  bind:this={el}
  aria-labelledby="{uid}-title"
  aria-modal="true"
  aria-busy={busy || undefined}
  class="m-auto max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] flex-col overflow-hidden rounded-lg border border-border bg-surface p-0 text-text shadow-[var(--shadow-overlay)] transition-[opacity,scale] duration-180 ease-out open:flex starting:open:scale-98 starting:open:opacity-0 {width[
    size
  ]} {sheet ? 'max-sm:h-dvh max-sm:max-h-none max-sm:w-full max-sm:max-w-none max-sm:rounded-none max-sm:border-0' : ''}"
  oncancel={(e) => {
    e.preventDefault();
    requestClose();
  }}
  onclose={closed}
  onkeydown={keydown}
  onclick={(e) => {
    // The panel fills the dialog box, so a click on the dialog itself is a click on the backdrop.
    if (e.target === el && !dirty) requestClose();
  }}
>
  <div
    class="flex min-h-0 flex-1 flex-col {sheet
      ? 'max-sm:grid max-sm:grid-cols-[auto_minmax(0,1fr)_auto] max-sm:grid-rows-[auto_minmax(0,1fr)] max-sm:items-center'
      : ''}"
  >
    <div class="flex items-start gap-3 px-5 pt-5 {sheet ? 'max-sm:contents' : ''}">
      <h2
        id="{uid}-title"
        class="min-w-0 flex-1 pt-1.5 text-heading text-text {sheet
          ? 'max-sm:col-start-2 max-sm:row-start-1 max-sm:truncate max-sm:px-2 max-sm:py-3.5 max-sm:text-center'
          : ''}"
      >
        {title}
      </h2>
      <IconButton icon="close" label="Close" size="sm" variant="quiet" class="-mt-0.5 -mr-2 {sheet ? 'max-sm:hidden' : ''}" disabled={busy} onclick={requestClose} />
    </div>
    {#if open}
      <div
        data-dialog-body
        class="min-h-0 flex-1 overflow-y-auto px-5 py-4 {sheet ? 'max-sm:col-span-3 max-sm:row-start-2 max-sm:self-stretch max-sm:border-t max-sm:border-border' : ''}"
      >
        {@render children()}
      </div>
      {#if footer}
        <!-- Phones: 440 dialogs stack full-width buttons, primary on top; sheets move Cancel and the primary into the header bar. -->
        <div
          data-dialog-footer
          class="flex justify-end gap-2 px-5 pb-5 {sheet
            ? 'max-sm:contents max-sm:[&>*]:row-start-1 max-sm:[&>*]:my-1 max-sm:[&>*]:px-3 max-sm:[&>:first-child:not(:last-child)]:col-start-1 max-sm:[&>:first-child:not(:last-child)]:ml-2 max-sm:[&>:last-child]:col-start-3 max-sm:[&>:last-child]:mr-2'
            : 'max-sm:flex-col-reverse max-sm:[&>*]:w-full'}"
        >
          {@render footer({ close: requestClose })}
        </div>
      {/if}
    {/if}
  </div>
</dialog>
