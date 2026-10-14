<script lang="ts">
  let { n, class: cls = '' }: { n: number; class?: string } = $props();
</script>

{#if n > 0}<span class="text-muted tabular-nums {cls}">{n.toLocaleString()}</span>{/if}
