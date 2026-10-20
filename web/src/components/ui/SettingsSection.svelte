<script lang="ts">
  import type { Snippet } from 'svelte';

  // A heading and its rows: 40 px above (none for the first), 8 px below, no subtitle (§11.1).
  // `action` is the section's one button ("Add…"), on the heading's line rather than under the card.
  let {
    title,
    bare = false,
    action,
    children,
    class: cls = '',
  }: { title: string; bare?: boolean; action?: Snippet; children: Snippet; class?: string } = $props();
  const uid = $props.id();
</script>

<section class="mt-10 first:mt-0 {cls}" aria-labelledby="{uid}-title">
  {#if action}
    <div class="flex min-h-8 items-center justify-between gap-4">
      <h2 id="{uid}-title" class="section-title">{title}</h2>
      {@render action()}
    </div>
  {:else}
    <h2 id="{uid}-title" class="section-title">{title}</h2>
  {/if}
  <div class="mt-2 {bare ? '' : 'list-card'}">{@render children()}</div>
</section>
