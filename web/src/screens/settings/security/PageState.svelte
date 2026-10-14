<script lang="ts">
  import { EmptyState } from '../../../components/ui';

  // The states a settings page shows instead of its content (§13.1, §13.2):
  // members on an admin page, a failed load and a slow one ("Loading…" only after 400 ms).
  let { kind, onretry }: { kind: 'forbidden' | 'failed' | 'loading'; onretry?: () => void } = $props();

  let slow = $state(false);
  $effect(() => {
    if (kind !== 'loading') return;
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
  });
</script>

{#if kind === 'forbidden'}
  <EmptyState text="Only admins can see this page." action={{ label: 'Go to chat', href: '/chat' }} />
{:else if kind === 'failed'}
  <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: onretry, variant: 'secondary' }} />
{:else}
  <p class="text-body text-muted" class:invisible={!slow} aria-busy="true">Loading…</p>
{/if}
