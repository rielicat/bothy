<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';

  // "Loading…" only after 400 ms; a failed load says so with one way to retry (§13.2).
  let { failed = false, onretry }: { failed?: boolean; onretry?: () => void } = $props();

  let slow = $state(false);
  onMount(() => {
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
  });
</script>

{#if failed}
  <EmptyState text="This page didn't load. Check your connection and try again." action={onretry ? { label: 'Try again', onclick: onretry, variant: 'secondary' } : undefined} />
{:else}
  <p class="text-body text-muted" class:invisible={!slow}>Loading…</p>
{/if}
