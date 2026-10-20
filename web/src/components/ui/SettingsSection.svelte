<script lang="ts">
  import type { Snippet } from 'svelte';

  // A heading and its rows: 40 px above (none for the first), 8 px below, no subtitle, no card (§11.1).
  let { title, bare = false, children, class: cls = '' }: { title: string; bare?: boolean; children: Snippet; class?: string } = $props();
  const uid = $props.id();
</script>

<section class="mt-10 first:mt-0 {cls}" aria-labelledby="{uid}-title">
  <h2 id="{uid}-title" class="section-title">{title}</h2>
  <div class="mt-2 {bare ? '' : 'list-card'}">{@render children()}</div>
</section>
