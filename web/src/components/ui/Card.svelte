<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';

  // Only approval cards and OptionList use a card; never nested, nothing bordered inside (§15.3 row 24).
  let { padding = 'md', class: cls = '', children, ...rest }: HTMLAttributes<HTMLDivElement> & { padding?: 'md' | 'lg'; class?: string; children: Snippet } = $props();
</script>

<div class="card {padding === 'lg' ? 'p-5' : ''} {cls}" {...rest}>{@render children()}</div>
