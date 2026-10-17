<script lang="ts" module>
  export interface Tab {
    label: string;
    href: string;
    count?: number;
  }

  /** The tab for the current address: an exact path-and-query match first, then the tab without a query on the same path. */
  export function currentTab(tabs: Tab[], path: string, search: string): string | undefined {
    const exact = tabs.find((t) => t.href === path + search);
    if (exact) return exact.href;
    return tabs.find((t) => !t.href.includes('?') && t.href === path)?.href;
  }
</script>

<script lang="ts">
  import { router } from '../../lib/router.svelte';

  // Route tabs are links with aria-current, not an ARIA tablist (§16).
  let { tabs, current, class: cls = '' }: { tabs: Tab[]; /** href of the current tab; defaults to the address */ current?: string; class?: string } = $props();

  const active = $derived(current ?? currentTab(tabs, router.path, router.search));

  // On a phone the row scrolls sideways: the current tab is brought into view,
  // and a fade at the right edge says more tabs follow.
  let row: HTMLDivElement | undefined = $state();
  let more = $state(false);
  const measure = () => {
    if (row) more = row.scrollWidth > row.clientWidth + 1 && row.scrollLeft + row.clientWidth < row.scrollWidth - 1;
  };
  $effect(() => {
    void active;
    const el = row;
    if (!el) return;
    // Scrolled by hand: scrollIntoView moves Chrome's sequential focus starting point, so the first Tab would skip "Skip to content".
    const cur = el.querySelector<HTMLElement>('[aria-current=page]');
    if (cur && el.scrollWidth > el.clientWidth) {
      const r = cur.getBoundingClientRect();
      const box = el.getBoundingClientRect();
      if (r.left < box.left) el.scrollLeft += r.left - box.left;
      else if (r.right > box.right) el.scrollLeft += r.right - box.right;
    }
    measure();
    el.addEventListener('scroll', measure, { passive: true });
    window.addEventListener('resize', measure);
    return () => {
      el.removeEventListener('scroll', measure);
      window.removeEventListener('resize', measure);
    };
  });
</script>

<div bind:this={row} class="tabs {more ? '[mask-image:linear-gradient(to_right,black_calc(100%-24px),transparent)]' : ''} {cls}">
  {#each tabs as t (t.href)}
    <a href={t.href} class="tab justify-center pointer-coarse:min-w-11" aria-current={t.href === active ? 'page' : undefined} onclick={router.link}
      >{t.label}{#if t.count}<span class="ml-1.5 font-normal text-muted tabular-nums">{t.count.toLocaleString()}</span>{/if}</a
    >
  {/each}
</div>
