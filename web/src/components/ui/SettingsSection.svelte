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
    <div class="flex items-center justify-between gap-4">
      <h2 id="{uid}-title" class="section-title">{title}</h2>
      <!-- Pulled into the heading's 22 px line, so the heading and its card keep the spacing of a section without a button.
           A 44 px touch target would then overlap the card, so on touch it only gives up 6 px each side. -->
      <div class="-my-[5px] shrink-0 pointer-coarse:-my-1.5">{@render action()}</div>
    </div>
  {:else}
    <h2 id="{uid}-title" class="section-title">{title}</h2>
  {/if}
  <div class="mt-2 {bare ? '' : 'list-card'}">{@render children()}</div>
</section>
