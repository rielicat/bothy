<script lang="ts">
  import { Button } from '../../components/ui';

  // "Loading…" only after 400 ms; a failed load says what to do (§13.2).
  let { failed = false, retry }: { failed?: boolean; retry: () => void } = $props();

  let slow = $state(false);
  $effect(() => {
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
  });
</script>

{#if failed}
  <div role="alert">
    <p class="text-reading text-text">This page didn't load. Check your connection and try again.</p>
    <Button class="mt-4" onclick={retry}>Try again</Button>
  </div>
{:else}
  <p class="text-body text-muted" class:invisible={!slow} aria-busy="true">Loading…</p>
{/if}
