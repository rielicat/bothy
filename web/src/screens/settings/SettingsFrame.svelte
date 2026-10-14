<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import { PageHeader } from '../../components/ui';
  import { router } from '../../lib/router.svelte';

  // The column and header of a settings page. Without its own back link a
  // group page gets "← Settings" below 1024 px, where the groups are a list
  // page instead of the sidebar (§4.3, §13.3).
  let {
    title,
    back,
    actions,
    loading = false,
    children,
  }: {
    title: string;
    back?: { href: string; label: string };
    actions?: Snippet;
    /** Shows "Loading…" instead of the content, and only after 400 ms (§13.2). */
    loading?: boolean;
    children: Snippet;
  } = $props();

  let slow = $state(false);
  $effect(() => {
    if (!loading) return;
    const t = setTimeout(() => (slow = true), 400);
    return () => {
      clearTimeout(t);
      slow = false;
    };
  });
</script>

<svelte:head><title>{title} · Bothy</title></svelte:head>

<div class="page">
  {#if !back}
    <a href="/settings" onclick={router.link} class="mb-2 inline-flex min-h-6 items-center gap-1 text-small text-muted hover:text-text lg:hidden pointer-coarse:min-h-11"
      ><Icon name="back" />Settings</a
    >
  {/if}
  <PageHeader {title} {back} {actions} />
  <div class="mt-6">
    {#if loading}
      <p class="text-body text-muted" class:invisible={!slow} aria-busy="true">Loading…</p>
    {:else}
      {@render children()}
    {/if}
  </div>
</div>
