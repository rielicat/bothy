<script lang="ts">
  import EmptyState from '../ui/EmptyState.svelte';

  // A screen whose chunk is still on its way: "Loading…" only after 400 ms
  // (docs/design/ui.md §13.2). Try again reloads, which also picks up a newer
  // build whose chunks replaced the ones this page asked for.
  let { failed = false }: { failed?: boolean } = $props();

  let slow = $state(false);
  $effect(() => {
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
  });
</script>

<div class="page">
  {#if failed}
    <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: () => window.location.reload(), variant: 'secondary' }} />
  {:else}
    <p class="text-body text-muted" class:invisible={!slow}>Loading…</p>
  {/if}
</div>
