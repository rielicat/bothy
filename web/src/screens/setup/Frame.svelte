<script lang="ts">
  import type { Snippet } from 'svelte';

  // The setup and sign-in frame (docs/design/ui.md §5.2). `card` puts sign-in and its
  // one-screen relatives in a card on the canvas (none on phones); setup steps keep one 480 px column.
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

{#if card}
  <main class="min-h-dvh bg-bg px-4 sm:bg-sidebar sm:px-6">
    <div class="mx-auto max-w-[26.5rem] pt-6 pb-16 sm:pt-[10vh]">
      <div class="sm:rounded-xl sm:border sm:border-border sm:bg-surface sm:p-9 sm:shadow-pop">
        <img src="/icon.svg" alt="" width="56" height="56" class="mx-auto size-14 rounded-lg dark:border dark:border-border-strong" />
        <h1 class="mt-5 text-center text-title text-text" tabindex="-1">{title}</h1>
        {#if intro}<p class="mt-2 text-center text-reading text-pretty text-muted">{intro}</p>{/if}
        <div class="mt-8 [&_.input]:h-11">{@render children()}</div>
      </div>
      {#if after}<div class="mt-6 text-center text-small text-muted">{@render after()}</div>{/if}
    </div>
  </main>
{:else}
  <main class="min-h-dvh bg-bg px-6 max-sm:px-4">
    <div class="mx-auto w-full max-w-[30rem] pt-[15vh] pb-16 max-sm:pt-6">
      <div class="flex items-center justify-between gap-4">
        <p class="flex items-center gap-2 text-heading text-text"><img src="/icon.svg" alt="" width="28" height="28" class="size-7" />Bothy</p>
        {#if step}<p class="text-small text-muted">Step {step} of 3</p>{/if}
      </div>
      <h1 class="mt-10 text-title text-text" tabindex="-1">{title}</h1>
      {#if intro}<p class="mt-2 text-reading text-pretty text-muted">{intro}</p>{/if}
      <div class="mt-8">{@render children()}</div>
    </div>
  </main>
{/if}
