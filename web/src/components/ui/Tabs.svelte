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
</script>

<div class="tabs {cls}">
  {#each tabs as t (t.href)}
    <a href={t.href} class="tab justify-center pointer-coarse:min-w-11" aria-current={t.href === active ? 'page' : undefined} onclick={router.link}
      >{t.label}{#if t.count}<span class="ml-1.5 font-normal text-muted tabular-nums">{t.count.toLocaleString()}</span>{/if}</a
    >
  {/each}
</div>
