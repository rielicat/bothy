<script lang="ts">
  import type { Snippet } from 'svelte';

  // The setup and sign-in frame (docs/design/ui.md §5.2): a card on the canvas, none on phones.
  // `card` is sign-in and its one-screen relatives (centred, 424 px); the setup steps are a wider
  // card led by the wordmark and the step, so the gate, the steps and sign-in read as one flow.
  let {
    title,
    intro,
    step,
    card = false,
    children,
    after,
  }: { title: string; intro?: string; step?: number; card?: boolean; children: Snippet; after?: Snippet } = $props();
</script>

<svelte:head><title>{title.includes('Bothy') ? title : `${title} · Bothy`}</title></svelte:head>

<main class="min-h-dvh bg-bg px-4 sm:bg-sidebar sm:px-6">
  <div class="mx-auto pt-6 pb-16 {card ? 'max-w-[26.5rem] sm:pt-[10vh]' : 'max-w-[34.5rem] sm:pt-[8vh]'}">
    <div class="sm:rounded-xl sm:border sm:border-border sm:bg-surface sm:p-9 sm:shadow-pop">
      {#if card}
        <img src="/icon.svg" alt="" width="56" height="56" class="mx-auto size-14 rounded-lg dark:border dark:border-border-strong" />
        <h1 class="mt-5 text-center text-title text-text" tabindex="-1">{title}</h1>
        {#if intro}<p class="mt-2 text-center text-reading text-pretty text-muted">{intro}</p>{/if}
      {:else}
        <div class="flex items-center justify-between gap-4">
          <p class="flex items-center gap-2 text-heading text-text"><img src="/icon.svg" alt="" width="28" height="28" class="size-7 rounded-sm dark:border dark:border-border-strong" />Bothy</p>
          {#if step}<p class="text-small text-muted">Step {step} of 3</p>{/if}
        </div>
        <h1 class="mt-8 text-title text-text" tabindex="-1">{title}</h1>
        {#if intro}<p class="mt-2 text-reading text-pretty text-muted">{intro}</p>{/if}
      {/if}
      <div class="mt-8 {card ? '[&_.input]:h-11' : ''}">{@render children()}</div>
    </div>
    {#if after}<div class="mt-6 text-center text-small text-muted">{@render after()}</div>{/if}
  </div>
</main>
