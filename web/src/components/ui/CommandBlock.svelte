<script lang="ts">
  import CodeBlock from './CodeBlock.svelte';

  // A single command, wrapped rather than scrolled so it is always read in full.
  // Words stay whole ("setup-token" never reads as two words); one longer than
  // the line still wraps, as an inline-block capped at the line's width.
  let { code, label, copy = true, class: cls = '' }: { code: string; label?: string; copy?: boolean; class?: string } = $props();
</script>

<CodeBlock {code} {label} {copy} wrap command class={cls} />
