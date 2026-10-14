<script lang="ts">
  import { untrack } from 'svelte';
  import { router } from '../../lib/router.svelte';
  import { shell } from '../../lib/stores/ui.svelte';
  import { focusables, trapTab } from '../ui/layer';
  import Sidebar from './Sidebar.svelte';

  // The sidebar as a drawer under 1024 px (docs/design/ui.md §4.4). The F2
  // Drawer goes full screen on phones; this one stays min(320px, 85vw) wide.
  let el: HTMLDialogElement | undefined = $state();
  let opener: HTMLElement | null = null;

  $effect(() => {
    const d = el;
    if (!d) return;
    if (shell.drawer && !d.open) {
      opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      d.showModal();
      (d.querySelector<HTMLElement>('[aria-current=page]') ?? focusables(d)[0])?.focus();
    } else if (!shell.drawer && d.open) d.close();
  });

  // Navigating closes it, and the new page takes focus rather than the menu button.
  $effect(() => {
    void router.path;
    untrack(() => {
      if (!shell.drawer) return;
      opener = null;
      shell.drawer = false;
      el?.close();
    });
  });

  // A modal left open while the window widens would keep the page inert behind the pinned sidebar.
  $effect(() => {
    const wide = window.matchMedia?.('(min-width: 1024px)');
    const close = () => wide?.matches && (shell.drawer = false);
    wide?.addEventListener('change', close);
    return () => wide?.removeEventListener('change', close);
  });
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_click_events_have_key_events -->
<dialog
  bind:this={el}
  aria-label="Menu"
  aria-modal="true"
  class="fixed inset-y-0 left-0 m-0 h-dvh max-h-none w-[min(320px,85vw)] max-w-none flex-col border-r border-border bg-sidebar p-0 text-text shadow-[var(--shadow-overlay)] transition-[translate] duration-200 ease-out open:flex starting:open:-translate-x-full lg:hidden"
  oncancel={(e) => {
    e.preventDefault();
    shell.drawer = false;
  }}
  onclose={() => {
    // A close event queued before a quick reopen must not close the new one.
    if (el?.open) return;
    shell.drawer = false;
    if (opener?.isConnected) opener.focus();
    opener = null;
  }}
  onkeydown={(e) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      shell.drawer = false;
      el?.close();
    } else if (el) trapTab(e, el);
  }}
  onclick={(e) => {
    if (e.target === el) shell.drawer = false;
  }}
>
  {#if shell.drawer}<Sidebar drawer />{/if}
</dialog>
