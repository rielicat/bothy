<script lang="ts">
  import type { Snippet } from 'svelte';

  // The setup and sign-in frame (docs/design/ui.md §5.2): no card, no border, one 480 px column.
  let { title, intro, step, children }: { title: string; intro?: string; step?: number; children: Snippet } = $props();
</script>

<svelte:head><title>{title.includes('Bothy') ? title : `${title} · Bothy`}</title></svelte:head>

<main class="min-h-dvh bg-bg">
  <div class="mx-auto w-full max-w-[30rem] px-6 pt-[15vh] pb-16 max-sm:px-4 max-sm:pt-6">
    <div class="flex items-baseline justify-between gap-4">
      <p class="text-heading text-text">Bothy</p>
      {#if step}<p class="text-small text-muted">Step {step} of 3</p>{/if}
    </div>
    <h1 class="mt-10 text-display text-text" tabindex="-1">{title}</h1>
    {#if intro}<p class="mt-2 text-reading text-muted">{intro}</p>{/if}
    <div class="mt-8">{@render children()}</div>
  </div>
</main>
