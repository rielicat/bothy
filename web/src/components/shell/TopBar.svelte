<script lang="ts">
  import { onMount } from 'svelte';
  import { router } from '../../lib/router.svelte';
  import { approvals } from '../../lib/stores/session.svelte';
  import { shell } from '../../lib/stores/ui.svelte';
  import Icon from '../Icon.svelte';
  import Button from '../ui/Button.svelte';
  import Dot from '../ui/Dot.svelte';

  // Phones and tablets (docs/design/ui.md §4.4). The title is the page's own
  // <title> ("Approvals · Bothy"), shown once the page's h1 has scrolled under
  // the bar or when the page has no visible h1 (a chat), so it never sits
  // right above the same words.
  let title = $state('');
  let covered = $state(true);
  let scrolled = $state(false);

  const n = $derived(approvals.waiting);
  const menuLabel = $derived(n ? `Open menu, ${n} ${n === 1 ? 'approval' : 'approvals'} waiting` : 'Open menu');

  onMount(() => {
    let frame = 0;
    const check = () => {
      frame = 0;
      title = document.title.replace(/\s*·\s*Bothy$/, '') || 'Bothy';
      const h1 = [...document.querySelectorAll('main h1')].map((h) => h.getBoundingClientRect()).find((r) => r.width > 1 && r.height > 1);
      covered = !h1 || h1.bottom <= 52;
      scrolled = window.scrollY > 0;
    };
    const soon = () => (frame ||= requestAnimationFrame(check));
    check();
    const watch = new MutationObserver(soon);
    watch.observe(document.head, { childList: true, subtree: true, characterData: true });
    const main = document.querySelector('main');
    if (main) watch.observe(main, { childList: true, subtree: true });
    window.addEventListener('scroll', soon, { passive: true });
    window.addEventListener('resize', soon);
    return () => {
      cancelAnimationFrame(frame);
      watch.disconnect();
      window.removeEventListener('scroll', soon);
      window.removeEventListener('resize', soon);
    };
  });
</script>

<div class="sticky top-0 z-30 flex h-13 items-center gap-1 border-b bg-bg px-2 sm:px-4 lg:hidden {scrolled ? 'border-border' : 'border-transparent'}">
  <button type="button" class="btn btn-ghost btn-icon relative" aria-label={menuLabel} aria-haspopup="dialog" aria-expanded={shell.drawer} onclick={() => (shell.drawer = true)}>
    <Icon name="menu" size={20} />
    {#if n}<Dot class="absolute top-2 right-2 pointer-coarse:top-3 pointer-coarse:right-3" label="" />{/if}
  </button>
  <p class="min-w-0 flex-1 truncate px-1 text-heading text-text {covered ? '' : 'invisible'}" aria-hidden={!covered}>{title}</p>
  {#if n && !router.path.startsWith('/approvals')}
    <Button variant="ghost" size="sm" href="/approvals" class="text-accent-text">{n} waiting</Button>
  {/if}
  {#if shell.actions}{@render shell.actions()}{/if}
</div>
