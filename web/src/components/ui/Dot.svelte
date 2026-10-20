<script lang="ts">
  let { label = 'Needs attention', size = 8, class: cls = '' }: { label?: string; size?: 6 | 8; class?: string } = $props();
</script>

<span class="inline-block shrink-0 rounded-full bg-warning {size === 6 ? 'size-1.5' : 'size-2'} {cls}"><span class="sr-only">{label}</span></span>
