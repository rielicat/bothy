<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';

  // "Loading…" only after 400 ms; a failed load says so with one way to retry (§13.2).
  // `text` names what didn't load when it isn't the page (a dialog's list, a search).
  let { failed = false, onretry, text = "This page didn't load. Check your connection and try again." }: { failed?: boolean; onretry?: () => void; text?: string } = $props();

  let slow = $state(false);
  onMount(() => {
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
  });
</script>

{#if failed}
  <EmptyState {text} action={onretry ? { label: 'Try again', onclick: onretry, variant: 'secondary' } : undefined} />
{:else}
  <p class="text-body text-muted" class:invisible={!slow}>Loading…</p>
{/if}
