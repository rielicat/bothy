<script lang="ts">
  import type { Snippet } from 'svelte';

  let { title = '', desc = '', children, actions }: { title?: string; desc?: string; children: Snippet; actions?: Snippet } = $props();
</script>

<section class="card mb-6 px-5">
  {#if title}
    <header class="flex flex-wrap items-start gap-3 border-b border-line py-4">
      <div class="min-w-0 flex-1">
        <h2 class="section-title">{title}</h2>
        {#if desc}<p class="help">{desc}</p>{/if}
      </div>
      {#if actions}<div class="flex flex-wrap gap-2">{@render actions()}</div>{/if}
    </header>
  {/if}
  <div class="divide-y divide-line">{@render children()}</div>
</section>
