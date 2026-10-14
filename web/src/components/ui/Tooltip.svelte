<script lang="ts">
  import type { Snippet } from 'svelte';
  import { tooltip } from './tooltip';

  // Never the only place information lives: touch devices never see it.
  let { text, placement = 'top', children }: { text: string; placement?: 'top' | 'bottom'; children: Snippet } = $props();
</script>

<span class="inline-flex" {@attach tooltip(() => text, { describe: true, placement })}>{@render children()}</span>
