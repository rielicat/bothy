<script lang="ts">
  import type { Snippet } from 'svelte';
  import { router } from '../../lib/router.svelte';
  import Icon from '../Icon.svelte';
  import Tabs, { type Tab } from './Tabs.svelte';

  let {
    title,
    back,
    actions,
    tabs,
    currentTab,
  }: {
    title: string;
    back?: { href: string; label: string };
    /** At most one primary or secondary button, plus an optional ⋯ menu (§6.2). */
    actions?: Snippet;
    tabs?: Tab[];
    currentTab?: string;
  } = $props();
</script>

<div>
  {#if back}
    <a
      href={back.href}
      onclick={router.link}
      class="mb-2 inline-flex min-h-6 items-center gap-1 text-small text-muted hover:text-text pointer-coarse:min-h-11"
      ><Icon name="back" />{back.label}</a
    >
  {/if}
  <!-- No subtitle. Actions sit on the title's baseline and wrap under it when they don't fit. -->
  <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-3">
    <h1 class="page-title min-w-0 break-words" tabindex="-1">{title}</h1>
    <!-- An icon-only ⋯ has no text baseline, so it would push the title down; it centres instead. -->
    {#if actions}<div class="flex shrink-0 items-center gap-2 [&:not(:has(.btn:not(.btn-icon)))]:self-center">{@render actions()}</div>{/if}
  </div>
  {#if tabs}<Tabs class="mt-4" {tabs} current={currentTab} />{/if}
</div>
