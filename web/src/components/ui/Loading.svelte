<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from './EmptyState.svelte';
  import Spinner from './Spinner.svelte';

  // "Loading…" only after 400 ms; a failed load says what didn't load, with one way to retry (§13.2).
  // `text` names it when it isn't the page: a dialog's list, a search.
  let {
    failed = false,
    retry,
    text = "This page didn't load. Check your connection and try again.",
  }: { failed?: boolean; retry?: () => void; text?: string } = $props();

  let slow = $state(false);
  onMount(() => {
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
  });
</script>

{#if failed}
  <div role="alert"><EmptyState {text} action={retry ? { label: 'Try again', onclick: retry, variant: 'secondary' } : undefined} /></div>
{:else}
  <p class="flex min-h-8 items-center gap-2 text-body text-muted" class:invisible={!slow}><Spinner class="motion-reduce:hidden" />Loading…</p>
{/if}
