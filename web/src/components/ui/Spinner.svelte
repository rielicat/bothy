<script lang="ts">
  import Icon from '../Icon.svelte';

  let { size = 16, label, class: cls = '' }: { size?: 12 | 16; label?: string; class?: string } = $props();
</script>

<!-- Under reduced motion the spinning arc becomes a static "…" (§3.6). -->
<span class="inline-flex shrink-0 items-center justify-center {size === 12 ? 'size-3' : 'size-4'} {cls}">
  <Icon name="spinner" {size} class="animate-spin motion-reduce:hidden" />
  <span class="hidden text-small leading-none motion-reduce:inline" aria-hidden="true">…</span>
  {#if label}<span class="sr-only">{label}</span>{/if}
</span>
