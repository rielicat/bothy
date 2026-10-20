<script lang="ts">
  import { api } from '../../lib/api/client';
  import type { StarterRow } from '../../lib/api/types.gen';
  import { router } from '../../lib/router.svelte';

  // The empty chat (§7.2), or with `bare` only its rows, under the welcome
  // message: up to four starters from the server. A row fills the composer
  // (never sends) or links to something not set up yet.
  let { onpick, bare = false }: { onpick: (text: string) => void; bare?: boolean } = $props();

  let rows: StarterRow[] = $state([]);
  $effect(() => {
    api.get<StarterRow[]>('/api/starters').then(
      (r) => (rows = r),
      () => (rows = [{ text: 'What can you do for our team?' }]),
    );
  });
</script>

<div class={bare ? '' : 'pt-[20vh] max-sm:pt-[12vh]'}>
  {#if !bare}
    <img src="/icon.svg" alt="" width="40" height="40" class="mb-4 size-10" />
    <h1 class="text-title text-text" tabindex="-1">What should Bothy work on?</h1>
    <p class="mt-2 max-w-[30rem] text-reading text-muted">Bothy can read your code, draft and research, and it asks before doing anything you'd want to check.</p>
  {/if}
  <!-- Room for four rows while they load, so the list doesn't push the page around. -->
  <ul class="mt-6 min-h-40">
    {#each rows as r (r.text)}
      <li>
        {#if r.href}
          <a
            href={r.href}
            onclick={router.link}
            class="-mx-3 flex min-h-10 items-center rounded-md px-3 text-body text-accent-text underline decoration-accent-text/40 underline-offset-2 hover:bg-subtle hover:decoration-accent-text pointer-coarse:min-h-11">{r.text}</a
          >
        {:else}
          <button
            type="button"
            class="-mx-3 flex min-h-10 w-[calc(100%+1.5rem)] cursor-pointer items-center rounded-md px-3 text-left text-body text-text hover:bg-subtle pointer-coarse:min-h-11"
            onclick={() => onpick(r.fill || r.text)}>{r.text}</button
          >
        {/if}
      </li>
    {/each}
  </ul>
</div>
