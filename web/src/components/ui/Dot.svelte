<script lang="ts">
  let { label = 'Needs attention', class: cls = '' }: { label?: string; class?: string } = $props();
</script>

<span class="inline-block size-2 shrink-0 rounded-full bg-warning-text {cls}"><span class="sr-only">{label}</span></span>
