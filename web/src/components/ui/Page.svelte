<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Tab } from './Tabs.svelte';
  import PageHeader from './PageHeader.svelte';

  // The one 768 px column every page shares, so content starts at x = 408 at 1280 px (§3.3).
  let {
    title,
    docTitle,
    back,
    actions,
    tabs,
    currentTab,
    children,
    class: cls = '',
  }: {
    title: string;
    /** The browser tab's title when it says more than the heading ("Spending · Activity"). */
    docTitle?: string;
    back?: { href: string; label: string };
    actions?: Snippet;
    tabs?: Tab[];
    currentTab?: string;
    children?: Snippet;
    class?: string;
  } = $props();
</script>

<svelte:head><title>{docTitle ?? title} · Bothy</title></svelte:head>

<div class="page {cls}">
  <PageHeader {title} {back} {actions} {tabs} {currentTab} />
  <div class="mt-6">{@render children?.()}</div>
</div>
