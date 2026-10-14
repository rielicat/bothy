<script lang="ts">
  // Only the keyboard shortcuts dialog uses keys (§6.4).
  let { keys }: { keys: string | string[] } = $props();
  const list = $derived(Array.isArray(keys) ? keys : [keys]);
</script>

<span class="inline-flex items-center gap-1">
  {#each list as k, i (i)}<kbd class="kbd">{k}</kbd>{/each}
</span>
