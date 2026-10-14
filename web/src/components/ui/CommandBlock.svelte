<script lang="ts">
  import CodeBlock from './CodeBlock.svelte';

  // A single command, wrapped rather than scrolled so it is always read in full.
  let { code, label, copy = true, class: cls = '' }: { code: string; label?: string; copy?: boolean; class?: string } = $props();
</script>

<CodeBlock {code} {label} {copy} wrap class={cls} />
